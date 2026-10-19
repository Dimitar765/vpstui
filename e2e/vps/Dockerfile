# e2e/vps/Dockerfile — throwaway "VPS": systemd Ubuntu + sshd + docker-in-docker.
# Built and run by e2e/vps/up.sh. Dockerd uses vfs (overlay-on-overlay is not
# supported when the container rootfs itself is on overlayfs). Only sshd is
# pre-enabled; install.sh's prep_docker enables and starts docker, per the README
# flow. docker-compose-v2 exists in noble; the fallback downloads the plugin.
FROM ubuntu:24.04
ARG SSH_PUBKEY
RUN apt-get update \
 && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
      ca-certificates curl openssh-server systemd docker.io \
 && (DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends docker-compose-v2 || true)
RUN docker compose version >/dev/null 2>&1 \
 || { mkdir -p /usr/local/lib/docker/cli-plugins \
      && curl -fsSL https://github.com/docker/compose/releases/latest/download/docker-compose-linux-x86_64 \
         -o /usr/local/lib/docker/cli-plugins/docker-compose \
      && chmod 0755 /usr/local/lib/docker/cli-plugins/docker-compose; }
RUN mkdir -p /etc/docker && printf '{"storage-driver":"vfs"}' > /etc/docker/daemon.json \
 && mkdir -p /run/sshd /root/.ssh \
 && printf '%s\n' "$SSH_PUBKEY" > /root/.ssh/authorized_keys \
 && chmod 0700 /root/.ssh && chmod 0600 /root/.ssh/authorized_keys \
 && mkdir -p /etc/systemd/system/multi-user.target.wants \
 && ln -sfn /lib/systemd/system/ssh.service /etc/systemd/system/multi-user.target.wants/ssh.service \
 && rm -rf /var/lib/apt/lists/*
STOPSIGNAL SIGRTMIN+3
CMD ["/lib/systemd/systemd"]
