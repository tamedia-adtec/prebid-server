FROM golang:1.21-alpine

RUN apk add --update tini 
RUN mkdir -p /app/prebid-server/
WORKDIR /app/prebid-server/

COPY ./ ./

RUN go mod download
RUN go mod tidy
RUN go mod vendor
RUN go build -mod=vendor -o /prebid-app

COPY static static/
COPY stored_requests/data stored_requests/data
RUN chmod -R a+r static/ stored_requests/data

RUN addgroup -g 29018 prebid-server
RUN adduser -D -H -u 29018 -G prebid-server prebid-server
RUN chown -R prebid-server:prebid-server /app/prebid-server/
USER prebid-server

EXPOSE 8000
EXPOSE 8001

ENTRYPOINT ["/sbin/tini", "--"]
CMD ["/prebid-app", "-v", "1", "-logtostderr"]

