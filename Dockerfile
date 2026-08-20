ARG NODE_IMAGE=node:24-bookworm
FROM ${NODE_IMAGE}

ARG BALDA_NPM_PACKAGE=@baldaworks/balda
ARG CODEX_NPM_PACKAGE=@openai/codex
ARG OPENCODE_NPM_PACKAGE=opencode-ai
ARG GEMINI_NPM_PACKAGE=@google/gemini-cli
ARG CLAUDE_CODE_NPM_PACKAGE=@anthropic-ai/claude-code
ARG COPILOT_NPM_PACKAGE=@github/copilot

RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      ca-certificates \
      curl \
      git \
      openssh-client \
      ripgrep \
 && rm -rf /var/lib/apt/lists/*

RUN npm install -g \
      "${BALDA_NPM_PACKAGE}" \
      "${CODEX_NPM_PACKAGE}" \
      "${OPENCODE_NPM_PACKAGE}" \
      "${GEMINI_NPM_PACKAGE}" \
      "${CLAUDE_CODE_NPM_PACKAGE}" \
      "${COPILOT_NPM_PACKAGE}" \
 && npm cache clean --force

RUN command -v balda \
 && command -v codex \
 && command -v opencode \
 && command -v gemini \
 && command -v claude \
 && command -v copilot \
 && ! command -v claudecode \
 && node --version \
 && npm --version \
 && npx --version \
 && git --version \
 && rg --version

USER node

WORKDIR /workspace
ENTRYPOINT ["balda"]
