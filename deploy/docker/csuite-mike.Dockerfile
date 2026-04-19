# syntax=docker/dockerfile:1.7
#
# drem-csuite-mike — COO persona.
#
# Layers CSUITE_AGENT=mike on top of drem-csuite-base. The base's
# entrypoint (/usr/local/bin/csuite-run.sh) reads this variable and
# execs the Claude CLI against /opt/csuite/prompts/mike.md.
#
# Build:
#   docker build -t localhost:5000/drem-csuite-mike:latest \
#     -f deploy/docker/csuite-mike.Dockerfile deploy/docker/context/

FROM localhost:5000/drem-csuite-base:latest

ENV CSUITE_AGENT=mike

# ENTRYPOINT and CMD are inherited from csuite-base (tini → csuite-run.sh).
