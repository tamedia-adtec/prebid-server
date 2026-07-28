FROM golang:1.26-alpine

# Install tini and required build dependencies
RUN apk add --no-cache tini gcc musl-dev

# Set up application directory
RUN mkdir -p /app/prebid-server/
WORKDIR /app/prebid-server/

# Copy application files
COPY ./ ./

# Build the Go application
ENV CGO_ENABLED=1
RUN go mod download
RUN go mod tidy
RUN go mod vendor
RUN go build -mod=vendor -o /prebid-app

# Copy static and data directories
COPY static static/
COPY stored_requests/data stored_requests/data

# Adjust permissions
RUN chmod -R a+r static/ stored_requests/data
RUN addgroup -g 29018 prebid-server
RUN adduser -D -H -u 29018 -G prebid-server prebid-server
RUN chown -R prebid-server:prebid-server /app/prebid-server/

# Set user to non-root
USER prebid-server

# Expose ports
EXPOSE 8000
EXPOSE 8001

# Define entrypoint and command
ENTRYPOINT ["/sbin/tini", "--"]
CMD ["/prebid-app", "-v", "1", "-logtostderr"]
