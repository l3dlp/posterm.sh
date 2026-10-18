# Caddy: let files dropped on a bash through

If the hub's Caddyfile block caps every request (here at 1 MB, like
`config/Caddyfile.example`), a bigger drop gets a 413. Over HTTP/3 the browser
doesn't even surface that error, and the upload bar freezes around 90 %.

## Replace

In the hub's block, the one with `reverse_proxy <POSTERM_ADDR>`:

```
	request_body {
		max_size 1MB
	}
```

## With

```
	# 1 MB everywhere, except files dropped on a bash.
	@upload path_regexp ^/m/[^/]+/api/labs/[^/]+/sh/upload$
	@rest not path_regexp ^/m/[^/]+/api/labs/[^/]+/sh/upload$
	request_body @rest {
		max_size 1MB
	}
	request_body @upload {
		max_size 10GB
	}
```

10 GB is an example: it's the largest single drop you allow.

## Then

```sh
caddy validate --config /path/to/Caddyfile --adapter caddyfile
caddy reload --config /path/to/Caddyfile --adapter caddyfile
```

Block checked with Caddy 2.7.6. `config/Caddyfile.example` already has it.
