# LADS OPC UA reference sample server (SPECTARIS / opcua-lads working group).
#
# There is no official published container image for the reference server, so we
# build one from the public LADS sample server sources:
#   https://github.com/opcua-lads/workshop
#
# Pin SIMULATOR_REF to a commit for reproducible acceptance runs.
FROM node:20-bookworm

ARG SIMULATOR_REPO=https://github.com/opcua-lads/workshop
# Pinned commit: the sample server publishes no release tags.
ARG SIMULATOR_REF=e653dbc

RUN git clone "${SIMULATOR_REPO}" /app && cd /app && git checkout "${SIMULATOR_REF}"
WORKDIR /app
RUN npm install

# The sample server hardcodes port 26543.
EXPOSE 26543
CMD ["npm", "start"]
