# A mobile network that holds back what phones send over UDP, early in a
# call, for the phone bench (TestPhoneBenchDocker, the "held" cases).
#
# The owner's iPhone on Etisalat (2026-10-16, captured at the server): for
# the first 30–45 s of each call, everything it sent over UDP stopped
# reaching Linx for 2.3 s at a time, then arrived in one burst, and about a
# second later it stopped again; what Linx sent it was never late, and its
# TCP — the line, ordinary uploads, the relay over TLS — went through.
#
# This does the same to every UDP packet from PEER (the address both
# simulators reach Linx from), through the kernel's NFQUEUE in the given
# containers' networks: nothing until SIGUSR1, then for FOR_S seconds
# PASS_MS through and HOLD_MS held, over and over, each hold's packets
# released together when it ends. TCP is never touched.
#
#   held_uploads.py PEER HOLD_MS PASS_MS FOR_S PID [PID…]
#
# Runs in a privileged container with the host's process namespace (each
# PID is a container's process, whose network it sits in). SIGTERM takes
# the rules out again.

import heapq
import os
import select
import signal
import socket
import struct
import subprocess
import sys
import time

NETLINK_NETFILTER = 12
QUEUE = 7
NFQNL_MSG_PACKET, NFQNL_MSG_VERDICT, NFQNL_MSG_CONFIG = 0, 1, 2
NFQA_PACKET_HDR = 1
NF_ACCEPT = 1

peer = sys.argv[1]
hold = int(sys.argv[2]) / 1000
passing = int(sys.argv[3]) / 1000
lasting = int(sys.argv[4])
pids = sys.argv[5:]


def attr(kind, data):
    n = 4 + len(data)
    return struct.pack("=HH", n, kind) + data + b"\0" * ((4 - n % 4) % 4)


def message(kind, attrs, flags=1):
    body = struct.pack("BB", socket.AF_UNSPEC, 0) + struct.pack("!H", QUEUE) + b"".join(attrs)
    return struct.pack("=IHHII", 16 + len(body), (3 << 8) | kind, flags, 0, 0) + body


RULE = ["INPUT", "-s", peer, "-p", "udp"]


def iptables(op):
    chain, *match = RULE
    subprocess.run(["iptables", op, chain, *match, "-j", "NFQUEUE", "--queue-num", str(QUEUE), "--queue-bypass"],
                   check=op == "-I")


home = os.open("/proc/self/ns/net", os.O_RDONLY)
queues = []
for pid in pids:
    ns = os.open(f"/proc/{pid}/ns/net", os.O_RDONLY)
    os.setns(ns, os.CLONE_NEWNET)
    s = socket.socket(socket.AF_NETLINK, socket.SOCK_RAW, NETLINK_NETFILTER)
    s.setsockopt(socket.SOL_SOCKET, socket.SO_RCVBUF, 8 << 20)
    s.bind((0, 0))
    s.send(message(NFQNL_MSG_CONFIG, [attr(1, struct.pack("!BBH", 1, 0, socket.AF_INET))], 1 | 4))  # bind
    s.recv(4096)
    s.send(message(NFQNL_MSG_CONFIG, [attr(2, struct.pack("!IB", 0, 1)),  # metadata only
                                      attr(3, struct.pack("!I", 50000))], 1 | 4))  # queue length
    s.recv(4096)
    iptables("-I")
    queues.append((s, ns))
os.setns(home, os.CLONE_NEWNET)

started = None
holds = 0
held = 0


def stop(*_):
    for s, ns in queues:
        os.setns(ns, os.CLONE_NEWNET)
        iptables("-D")
    print(f"held: stopped; {holds} holds, {held} packets held", flush=True)
    sys.exit(0)


def begin(*_):
    global started
    started = time.monotonic()
    print("held: the network starts holding UDP back", flush=True)


signal.signal(signal.SIGTERM, stop)
signal.signal(signal.SIGINT, stop)
signal.signal(signal.SIGUSR1, begin)
print(f"held: on for {peer}: {hold * 1000:.0f} ms held, {passing * 1000:.0f} ms through, for {lasting} s",
      flush=True)

waiting = []  # (release, order, socket, packet id)
order = 0
last_hold = -1


def when(now):
    """When a UDP packet from the phones that arrives now goes on."""
    global holds, last_hold
    if started is None or now - started >= lasting:
        return now
    cycle = passing + hold
    k, phase = divmod(now - started, cycle)
    if phase < passing:
        return now
    if k != last_hold:
        last_hold = k
        holds += 1
        print(f"held: hold {holds} at +{now - started:.1f} s", flush=True)
    return started + (k + 1) * cycle


def verdict(s, pid):
    s.send(message(NFQNL_MSG_VERDICT, [attr(2, struct.pack("!II", NF_ACCEPT, pid))]))


socks = [s for s, _ in queues]
while True:
    timeout = max(0.0, waiting[0][0] - time.monotonic()) if waiting else 1.0
    try:
        ready, _, _ = select.select(socks, [], [], timeout)
    except InterruptedError:
        continue
    for s in ready:
        try:
            data = s.recv(1 << 17)
        except OSError:  # ENOBUFS: the queue overflowed, the kernel moves on
            continue
        off = 0
        while off + 16 <= len(data):
            n, kind, _, _, _ = struct.unpack_from("=IHHII", data, off)
            if n < 16:
                break
            if kind == (3 << 8) | NFQNL_MSG_PACKET:
                pid = None
                a = off + 20
                while a + 4 <= off + n:
                    alen, akind = struct.unpack_from("=HH", data, a)
                    if alen < 4:
                        break
                    if akind & 0x7FFF == NFQA_PACKET_HDR:
                        (pid,) = struct.unpack_from("!I", data, a + 4)
                    a += (alen + 3) & ~3
                if pid is not None:
                    now = time.monotonic()
                    at = when(now)
                    if at <= now:
                        verdict(s, pid)
                    else:
                        held += 1
                        order += 1
                        heapq.heappush(waiting, (at, order, s, pid))
            off += (n + 3) & ~3
    now = time.monotonic()
    while waiting and waiting[0][0] <= now:
        _, _, s, pid = heapq.heappop(waiting)
        verdict(s, pid)
