FROM gcr.io/distroless/static:latest@sha256:448810357cc8e67b7208988abdba0f5ec3d3aebee0a5b13eb624979792cffc0f

ARG TARGETOS
ARG TARGETARCH

ADD bin/$TARGETOS\_$TARGETARCH/provider /usr/local/bin/crossplane-yukimi-provider

USER 65532
ENTRYPOINT ["crossplane-yukimi-provider"]
