# syntax=docker/dockerfile:1.7
#
# drem-csuite-seth — CTO persona.
#
# Layers CSUITE_AGENT=seth on top of drem-csuite-base. The base's
# entrypoint (/usr/local/bin/csuite-run.sh) reads this variable and
# execs the Claude CLI against /opt/csuite/prompts/seth.md.
#
# Build:
#   docker build -t localhost:5000/drem-csuite-seth:latest \
#     -f deploy/docker/csuite-seth.Dockerfile deploy/docker/context/

FROM localhost:5000/drem-csuite-base:latest

ENV CSUITE_AGENT=seth

# ENTRYPOINT and CMD are inherited from csuite-base (tini → csuite-run.sh).
