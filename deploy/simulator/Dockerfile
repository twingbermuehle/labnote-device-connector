# LADS OPC UA reference sample server (SPECTARIS / opcua-lads working group).
#
# There is no official published container image for the reference server, so we
# build one from the public LADS sample server sources:
#   https://github.com/opcua-lads/workshop
#
# Pin SIMULATOR_REF to a commit for reproducible acceptance runs.
FROM node:20-bookworm

ARG SIMULATOR_REPO=https://github.com/opcua-lads/workshop
ARG SIMULATOR_REF=main

RUN git clone --depth 1 --branch "${SIMULATOR_REF}" "${SIMULATOR_REPO}" /app
WORKDIR /app
RUN npm install

EXPOSE 4840
CMD ["npm", "start"]
