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
    && rm -rf /var/lib/apt/lists/*

# Node.js (LTS) for OpenCode CLI.
RUN curl -fsSL https://deb.nodesource.com/setup_22.x | bash - \
    && apt-get install -y nodejs \
    && rm -rf /var/lib/apt/lists/*

# OpenCode CLI.
RUN npm install -g opencode-ai

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

# Create required directories.
RUN mkdir -p /agent/output /agent/workspace && \
    chown -R agent:agent /agent

# Copy entrypoint.
COPY entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod +x /usr/local/bin/entrypoint.sh

USER agent
WORKDIR /agent/workspace

ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
