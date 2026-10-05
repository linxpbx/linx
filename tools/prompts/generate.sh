#!/bin/sh
# Reads /in/prompts.tsv, writes /out/<name>.g722 (16 kHz G.722, the format
# Asterisk plays without converting for most phones) and /out/SHA256SUMS.
set -eu
# Work in /tmp: the tool leaves files in its working folder.
cd /tmp
rm -f /out/SHA256SUMS
# A message that already has a file is left exactly as it is. Speaking is
# not bit-for-bit repeatable (the voice adds a little noise of its own),
# so regenerating everything would quietly re-record messages nobody
# asked to change — including ones the owner has already heard and
# approved. Adding a line here speaks that one; to change the words of an
# existing message, delete its .g722 first and run make prompts again.
grep -v '^#' /in/prompts.tsv | grep -v '^$' | while IFS="$(printf '\t')" read -r name text; do
  if [ -f "/out/$name.g722" ]; then
    echo "$name: already here"
    continue
  fi
  printf '%s\n' "$text" | python -m piper -m /voice/en_US-kristin-medium.onnx -f "/tmp/$name.wav" --sentence-silence 0.3
  # A quarter second of silence first so the start isn't clipped as the
  # call's audio begins; loudness brought to a steady telephone level.
  ffmpeg -nostdin -loglevel error -y -i "/tmp/$name.wav" -af "adelay=250,loudnorm=I=-18:TP=-3" -ar 16000 -ac 1 -c:a g722 -f g722 "/out/$name.g722"
  echo "$name: $text"
done
# The voicemail tone (internal/asteriskconf's linx-voicemail): half a
# second of 1 kHz, not a voice.
if [ ! -f /out/beep.g722 ]; then
  ffmpeg -nostdin -loglevel error -y -f lavfi -i "sine=frequency=1000:duration=0.5" -af "volume=-12dB" -ar 16000 -ac 1 -c:a g722 -f g722 /out/beep.g722
fi
echo "beep: (tone)"
cd /out && sha256sum ./*.g722 > SHA256SUMS
