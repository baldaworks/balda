FROM node:lts-bookworm

RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      ca-certificates \
      curl \
      git \
      openssh-client \
 && rm -rf /var/lib/apt/lists/*

RUN npm install -g @normahq/relay \
 && npm cache clean --force

# Install the provider CLI used by relay.provider here when building a custom
# runtime image, for example Codex, Gemini, Claude Code, opencode, Copilot, or
# another ACP-compatible command.

RUN node --version && npm --version && npx --version && git --version

WORKDIR /workspace
ENTRYPOINT ["relay"]
