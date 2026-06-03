#!/usr/bin/env bash
go build -ldflags "-X main.version=$VERSION -X main.commit=$COMMIT" -o dist/freshbreath-windows-x64.exe .
