# syntax=docker/dockerfile:1.7
#
# drem-csuite-kyle — CEO persona.
#
# Layers CSUITE_AGENT=kyle on top of drem-csuite-base. The base's default
# CMD (/usr/local/bin/csuite-entrypoint) reads this variable and execs
# the csuite-persona poller against /opt/csuite/prompts/kyle.md.
#
# Unlike the other three personas (Mike, Alex, Seth), Kyle runs with a
# `:rw` bind-mount on the shared orch-plans tree — he is the plan-author
# in the C-Suite and needs to edit plan docs without delegating through
# a worker. The mount policy is enforced by the per-project compose
# template, not this Dockerfile; see
# internal/projects/templates/project-compose.yml.tmpl.
#
# Build:
#   docker build -t localhost:5000/drem-csuite-kyle:latest \
#     -f deploy/docker/csuite-kyle.Dockerfile deploy/docker/context/

FROM localhost:5000/drem-csuite-base:latest

ENV CSUITE_AGENT=kyle

# ENTRYPOINT and CMD are inherited from csuite-base (tini → csuite-entrypoint).
