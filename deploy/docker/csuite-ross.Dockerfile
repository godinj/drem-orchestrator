# syntax=docker/dockerfile:1.7
#
# drem-csuite-ross — HR persona.
#
# Layers CSUITE_AGENT=ross on top of drem-csuite-base. The base's
# entrypoint (/usr/local/bin/csuite-run.sh) reads this variable and
# execs the Claude CLI against /opt/csuite/prompts/ross.md.
#
# Build:
#   docker build -t localhost:5000/drem-csuite-ross:latest \
#     -f deploy/docker/csuite-ross.Dockerfile deploy/docker/context/

FROM localhost:5000/drem-csuite-base:latest

ENV CSUITE_AGENT=ross

# ENTRYPOINT and CMD are inherited from csuite-base (tini → csuite-run.sh).
