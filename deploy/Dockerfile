# Supply a reviewed Node 24 image with @sha256 digest; never build from latest.
ARG NODE_IMAGE
FROM ${NODE_IMAGE}
WORKDIR /app
COPY --chown=node:node package.json ./
COPY --chown=node:node src ./src
COPY --chown=node:node config ./config
RUN mkdir -p /app/var && chown node:node /app/var
USER node
ENV HOST=0.0.0.0 PORT=8090 DATABASE_PATH=/app/var/governance.db
EXPOSE 8090
CMD ["node", "src/main.ts"]
