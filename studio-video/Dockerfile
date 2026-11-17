FROM node:20.20.2-bookworm-slim

LABEL org.opencontainers.image.source="https://github.com/aldiCovo/AldiApps"

# Claude Code for the subscription mode: with no ANTHROPIC_API_KEY the server runs `claude -p`,
# logged in by CLAUDE_CODE_OAUTH_TOKEN from a Secret.
RUN npm install -g @anthropic-ai/claude-code@2.1.324 && npm cache clean --force

WORKDIR /app
RUN chown node:node /app
USER node

# devDependencies too: the server runs `tsc --noEmit` on the code Claude writes, and starts with tsx.
# Rendering is done by the worker on another computer, so there is no Chrome or ffmpeg here.
COPY --chown=node:node package.json package-lock.json ./
RUN npm ci && npm cache clean --force

COPY --chown=node:node . .

# The app rewrites .env when TikTok rotates its refresh token and when a login script saves one,
# so .env lives on the volume (/data/config) and stays writable. src/generated is rewritten for
# every video and stays in the container.
RUN ln -s /data/config/.env /app/.env

ENV NODE_ENV=production \
    HOME=/data/home \
    STUDIO_HOST=0.0.0.0 \
    STUDIO_PORT=4321 \
    RENDER_MODE=remote

EXPOSE 4321
CMD ["npx", "tsx", "automation/server.ts", "--no-open"]
