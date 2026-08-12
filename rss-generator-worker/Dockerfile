FROM node:22-bookworm-slim AS build
ENV PNPM_HOME=/pnpm
ENV PATH=$PNPM_HOME:$PATH
RUN corepack enable
WORKDIR /app
COPY package.json pnpm-lock.yaml tsconfig.json ./
RUN pnpm install --frozen-lockfile
COPY src ./src
RUN pnpm build && pnpm prune --prod

FROM mcr.microsoft.com/playwright:v1.62.1-noble
ENV NODE_ENV=production
ENV HOST=0.0.0.0
ENV PORT=8787
WORKDIR /app
COPY --from=build /app/node_modules ./node_modules
COPY --from=build /app/dist ./dist
COPY package.json ./package.json
USER pwuser
EXPOSE 8787
CMD ["node", "dist/src/index.js"]
