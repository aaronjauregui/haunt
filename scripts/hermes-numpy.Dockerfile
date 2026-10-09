# The hermes image with numpy, for the holographic memory provider's
# vector recall (without it, holographic falls back to keyword search).
# scripts/hermes-docker uses hermes-agent:<pin>-numpy over the plain pin
# when it exists:
#
#   docker build -t hermes-agent:818c13b-numpy - < scripts/hermes-numpy.Dockerfile
#
# The venv has no pip; the image's own uv installs into it, at the numpy
# version hermes's pyproject.toml pins for its extras.
ARG BASE=hermes-agent:818c13b
FROM ${BASE}
RUN /opt/hermes/tools/uv-0.12.3-linux-x64/uv pip install --no-cache \
    --python /opt/hermes/.venv/bin/python numpy==2.4.3
