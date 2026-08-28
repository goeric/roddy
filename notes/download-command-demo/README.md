# Testing roddy download command

*2026-02-10T15:00:57Z*

The most recent commit (d82a926) added `file` and `download` commands to roddy. The `download` command fetches the href or src target of an element and saves it to a file. It supports regular URLs, data: URLs, and can output to stdout with `-`. Let's exercise it.

First, start a headless browser and create a small test page with various downloadable elements.

```bash
go run . start 2>/dev/null && go run . status
```

```output
Chrome started
Browser running
Pages: 0
Active page: 0
```

Navigate to our test page which has a regular link, a data: URL link, and an image element.

```bash
go run . open http://localhost:8765/index.html
```

```output
Download Test Page
```

```bash
go run . html
```

```output
<html lang="en"><head><title>Download Test Page</title></head>
<body>
  <h1>Download Test Page</h1>
  <a id="text-link" href="/hello.txt">Download text file</a><br>
  <a id="data-link" href="data:text/plain;base64,SGVsbG8gZnJvbSBhIGRhdGEgVVJMIQ==">Download data URL</a><br>
  <img id="pixel-img" src="data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPj/HwADBwIAMCbHYQAAAABJRU5ErkJggg==" alt="1x1 pixel">


</body></html>
```

## Download a regular URL via href

Download the text file linked by the `#text-link` anchor element. This exercises the fetch()-based download path that runs in the page context.

```bash
go run . download "#text-link" /tmp/downloaded.txt && cat /tmp/downloaded.txt
```

```output
Saved /tmp/downloaded.txt (33 bytes)
Hello from roddy download test!
```

## Download a data: URL

Download the content of a `data:` URL link. This exercises the `decodeDataURL` code path that handles base64-encoded inline data.

```bash
go run . download "#data-link" /tmp/data-download.txt && cat /tmp/data-download.txt
```

```output
Saved /tmp/data-download.txt (22 bytes)
Hello from a data URL!
```

## Download from an img src attribute

Download the data embedded in an `<img>` element's `src` attribute. This confirms the command reads `src` when no `href` is present, and handles binary data (a 1x1 PNG pixel).

```bash
go run . download "#pixel-img" /tmp/pixel.png && file /tmp/pixel.png && wc -c < /tmp/pixel.png
```

```output
Saved /tmp/pixel.png (70 bytes)
/tmp/pixel.png: PNG image data, 1 x 1, 8-bit/color RGBA, non-interlaced
70
```

## Download to stdout

Use `-` as the output file to pipe the downloaded content to stdout. This is useful for piping into other commands.

```bash
go run . download "#text-link" - | wc -c
```

```output
33
```

## Auto-inferred filename

When no output file is specified, the download command infers a filename from the URL. Let's test that by downloading without specifying a filename.

```bash
cd /tmp && /tmp/roddy download "#text-link" && cat /tmp/hello.txt
```

```output
Saved hello.txt (33 bytes)
Hello from roddy download test!
```

The filename `hello.txt` was inferred from the URL path `/hello.txt`. All download code paths work correctly.
