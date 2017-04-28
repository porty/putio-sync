# Temporary Repo

The fix in this repo should be upstreamed, and will be in time. `go get` from here to get a working copy though!

# putio-sync

putio-sync is a cross-platform command line program to download your preferred Put.io directory to your computer.

It periodically watches for changes and downloads new content when they arrive.

![](https://s3-eu-west-1.amazonaws.com/putio-mac-app/putio-sync-ss.png)

## Features

* Cross platform
* Synchronizes upstream folder to make directory hiearchy identical
* Checks for file hash equality
* Pause/resume support
* HTTP API for external clients
* Web UI

# Installation

If you have Go installed, try checking out the latest release:

```sh
go get -u github.com/porty/putio-sync
```

# Usage

Run `putio-sync -server` and visit `http://127.0.0.1:3000`

# License

MIT. See LICENSE file.
