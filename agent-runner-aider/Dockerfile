FROM ubuntu:24.04@sha256:cd1dba651b3080c3686ecf4e3c4220f026b521fb76978881737d24f200828b2b

ENV DEBIAN_FRONTEND=noninteractive

# Core system dependencies.
RUN apt-get update && apt-get install -y \
    git \
    curl \
    wget \
    ca-certificates \
    gnupg \
    jq \
    python3 \
    python3-pip \
    python3-venv \
    && rm -rf /var/lib/apt/lists/*

# GitHub CLI.
RUN curl -fsSL https://cli.github.com/packages/githubcli-archive-keyring.gpg \
    | dd of=/usr/share/keyrings/githubcli-archive-keyring.gpg \
    && echo "deb [arch=$(dpkg --print-architecture) signed-by=/usr/share/keyrings/githubcli-archive-keyring.gpg] https://cli.github.com/packages stable main" \
    | tee /etc/apt/sources.list.d/github-cli.list > /dev/null \
    && apt-get update \
    && apt-get install -y gh \
    && rm -rf /var/lib/apt/lists/*

# Create non-root user (remove default ubuntu user that occupies UID 1000).
RUN userdel -r ubuntu 2>/dev/null || true \
    && groupadd -g 1000 agent \
    && useradd -m -u 1000 -g agent agent

# Install Aider in a virtual environment owned by the agent user.
USER agent
RUN python3 -m venv /home/agent/.venv \
    && /home/agent/.venv/bin/pip install --no-cache-dir aider-chat
ENV PATH="/home/agent/.venv/bin:$PATH"
USER root

# Create required directories.
RUN mkdir -p /agent/output /agent/workspace && \
    chown -R agent:agent /agent

# Copy entrypoint.
COPY entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod +x /usr/local/bin/entrypoint.sh

USER agent
WORKDIR /agent/workspace

ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
