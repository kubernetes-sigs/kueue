FROM golang:1.26.0 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY main.go .
RUN CGO_ENABLED=0 go build -o /out/populator main.go

FROM gcr.io/distroless/static:nonroot
COPY --from=build /out/populator /populator
USER 65532:65532
ENTRYPOINT ["/populator"]
