# Makes Linx's call messages (make prompts): a development tool only, never
# part of a Linx image. Piper (piper-tts, GPL-3.0) and its phonemizer run
# here to turn tools/prompts/prompts.tsv into audio; only the audio files
# are committed (deploy/docker/asterisk/prompts/), and they aren't covered
# by the program's licence. Voice: "kristin", trained from scratch on
# public-domain LibriVox recordings (VOICE_MODEL_CARD), from the MIT-licensed
# rhasspy/piper-voices at a pinned revision, each file pinned by SHA-256.
FROM python:3.12-slim-bookworm@sha256:392307d22300de8b5986851a12d9176dfc0fc073e65bf6523ebd7dcbeb23564e
RUN apt-get update && apt-get install -y --no-install-recommends ffmpeg && rm -rf /var/lib/apt/lists/*
COPY requirements.txt /tmp/requirements.txt
RUN pip install --no-cache-dir --require-hashes -r /tmp/requirements.txt
ARG VOICES=https://huggingface.co/rhasspy/piper-voices/resolve/c10ece1aade47bb51c153c893d14e5bf8e5b7117/en/en_US/kristin/medium
ADD --chmod=0444 --checksum=sha256:5849957f929cbf720c258f8458692d6103fff2f0e3d3b19c8259474bb06a18d4 ${VOICES}/en_US-kristin-medium.onnx /voice/
ADD --chmod=0444 --checksum=sha256:5681426d4aead22195de70531eeeeddb46493cfaffc5764b2ea3db73428b651c ${VOICES}/en_US-kristin-medium.onnx.json /voice/
RUN chmod 0755 /voice
COPY generate.sh /generate.sh
USER 65532:65532
ENTRYPOINT ["sh", "/generate.sh"]
