# File storage

Files can be stored locally, in S3, or through a file server. `cmd/bucket` is a small file host for a shared database: other instances send files to it with `PUT /files/...` and a bearer token. See [tools/bucket/README.md](../tools/bucket/README.md).

