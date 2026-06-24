# BTST engine — multi-stage build. modernc.org/sqlite is pure-Go, so no CGO /
# gcc is needed and the final image can be tiny.
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /out/btst ./cmd/btst

FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata
WORKDIR /app
COPY --from=build /out/btst /app/btst
# Persistent data (SQLite). Mount a Render disk here to survive restarts.
RUN mkdir -p /app/data
ENV BTST_DB=/app/data/btst.db
ENV PORT=8085
EXPOSE 8085
ENTRYPOINT ["/app/btst"]
