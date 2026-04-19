# syntax=docker/dockerfile:1.7
#
# drem-worker-cpp — C/C++-language worker image.
#
# Layered on top of drem-worker-base (deploy/docker/worker-base.Dockerfile).
# Adds a standard GCC/Clang build surface sufficient for CMake+Ninja projects
# (drem-canvas is the initial consumer — PRD user stories 32, 43).
#
# Explicitly out of scope for this image per the prompt:
#   - vcpkg / conan / CUDA / cross-compile toolchains. Add in a project-specific
#     child image if needed.
#
# Build:
#   docker build -t localhost:5000/drem-worker-cpp:latest \
#     -f deploy/docker/worker-cpp.Dockerfile deploy/docker/context/

FROM localhost:5000/drem-worker-base:latest

# ---- version pins ---------------------------------------------------------
# Debian bookworm ships reproducible toolchain versions; we rely on apt
# metadata for SHA verification. Bumping the distro (base image) implies
# bumping these in lockstep.
ARG DEBIAN_FRONTEND=noninteractive

USER root

# ---- C/C++ toolchain ------------------------------------------------------
# build-essential pulls gcc, g++, make, libc6-dev. cmake + ninja-build cover
# modern build-system needs. gdb is included for local debugging sessions an
# agent may launch via Bash. clang-format / clang-tidy live in separate apt
# packages on bookworm.
RUN apt-get update \
 && apt-get install -y --no-install-recommends \
        build-essential \
        clang \
        clang-format \
        clang-tidy \
        cmake \
        gdb \
        make \
        ninja-build \
        pkg-config \
 && rm -rf /var/lib/apt/lists/*

# ---- language marker ------------------------------------------------------
# Read by the spawner / project-registry dispatcher (PRD user story 33).
ENV DREM_LANGUAGE=cpp

USER drem
WORKDIR /home/drem/work

# ENTRYPOINT and CMD are inherited from the base (tini → worker-entrypoint).
