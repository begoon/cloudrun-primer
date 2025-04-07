# GCP Cloud Run primer

[![Run on Google Cloud](https://deploy.cloud.run/button.svg)](https://deploy.cloud.run?git_repo=https://github.com/begoon/cloudrun-primer)

This demo application can be used as a stub image for newly created Cloud Run services.

The application is written in Go.

The default page prints the environment variables and a few critical GCP metadata, such as "project," "zone," etc.

Additionally, the application exposes a few extra endpoints:

- `/speed` - to measure download speed from [ipv4.download.thinkbroadband.com](http://ipv4.download.thinkbroadband.com)
- `/ip` - to show the service public internet IP
- `/fs` - to browse the service file system
- `/ls` - to browse the service file system with more details

Note: The application is for educational purposes.
