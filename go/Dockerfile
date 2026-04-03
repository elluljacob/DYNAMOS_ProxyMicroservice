FROM golang:1.23-bullseye AS builder
# FROM test:latest AS builder
ARG NAME
ENV NAME=$NAME

# Set the working directory
WORKDIR /app/cmd/$NAME
RUN mkdir /app/pkg
RUN mkdir /app/etcd

# Copy all files from the service directory (including subdirectories like repository/, service/)
COPY . ./

# Remove go.mod/go.sum from service dir (they get copied here by Makefile but must only exist at /app)
RUN rm -f go.mod go.sum
# Remove pkg from service dir (it also gets copied by Makefile but must only exist at /app)
RUN rm -rf pkg

# Copy go.mod and go.sum files into the container
COPY go.mod /app
COPY go.sum /app

# # Copy the /pkg directory
COPY pkg /app/pkg

# Build the application
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o /go/bin/$NAME .

# 2. Running the Application in a Small Image
FROM alpine:3.18.2
ARG NAME
ENV NAME=$NAME
RUN apk --no-cache add ca-certificates

WORKDIR /app/

# Copy the pre-built binary from the previous stage
COPY --from=builder /go/bin/$NAME .

# Basic healthcheck: ensure the service process is running
HEALTHCHECK --interval=30s --timeout=3s --retries=3 CMD /bin/sh -c "ps | grep -q \"[\\/]app/$NAME\""

# Start the application
CMD ["/bin/sh", "-c", "/app/$NAME || ls -al /app"]
