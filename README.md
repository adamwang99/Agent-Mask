# Agent Mask

**Drop in a folder of videos and screenshots. Get back the same folder with every email, phone number, API key and password hidden, and each file checked again.**

Free, open source (MIT), and it runs entirely on your computer: nothing is uploaded anywhere.

[Tiếng Việt bên dưới](#tiếng-việt)

---

## Why

You recorded twenty tutorials, a sprint demo or a bug report, and somewhere in there a `.env`
file flashed by, a customer's email sat in a sidebar, or a token showed in a terminal for half
a second. Scrubbing a recording by hand means scrubbing every frame, and one missed frame is a leak.

Agent Mask reads the text on screen, frame by frame, finds what should not be shared, hides it
**while it moves** (scrolling, typing, windows sliding), and then **reads its own output again**.
A file is only marked *verified* when that second pass finds nothing. Anything else goes to a
`_review/` folder, so a doubtful result can never be shared by mistake.

## What it does

- **A whole folder at once.** Videos (mp4, mov, mkv, webm, avi, m4v, wmv) and pictures (png, jpg,
  webp, bmp, tif), sub-folders kept as they are.
- **Finds what matters:** emails, phone numbers, API keys and tokens (OpenAI, Anthropic, AWS, GitHub,
  GitLab, Google, Slack, Stripe, npm…), JWTs, private keys, `PASSWORD=…` style secrets, passwords in URLs, card numbers,
  long random strings, in English and Vietnamese.
- **Hides it well.** Secrets under a flat shade of their own background, personal data under a
  mosaic; masks follow the text as it scrolls. Styles: `auto`, `soft`, `mosaic`, `blur`, `solid`.
- **Checks itself.** Every output is scanned again. *Verified* is earned, never assumed: an OCR
  that reads nothing does not vouch for anything.
- **Tells you what happened.** `report.json` for programs, `report.md` for people: each file, its
  result, how many things were hidden and of what kind. The hidden text itself is never written down.
- **Skips what is done.** Run it again and unchanged files are skipped in a second.
- **Watches a folder** if you want: drop a file in, get it masked a moment later.
- **Made for agents.** An MCP server for Claude Code, Codex, Cursor and others; a local HTTP API;
  callbacks, commands and desktop notices when a job ends.

## Quick start

1. **Install.**
   - Windows: run `AgentMask-Setup-<version>.exe` from [Releases](https://github.com/adamwang99/agent-mask/releases). It offers to install ffmpeg and Tesseract for you.
   - macOS: `brew install ffmpeg tesseract`, then unpack `agent-mask-<version>-darwin-arm64.tar.gz` (or `-amd64` for Intel).
   - Linux: `sudo apt install ffmpeg tesseract-ocr`, then unpack the `linux` archive.
   - From source: `go install github.com/adamwang99/agent-mask/cmd/agent-mask@latest` (Go 1.24+).
2. **Check:** `agent-mask doctor`
3. **Run:**

```bash
agent-mask batch ~/Videos/raw ~/Videos/safe
```

```
Agent Mask: 4 file(s) from /home/me/Videos/raw to /home/me/Videos/safe
[1/4] verified demo.mp4 (1 hidden, 12.0s)
[2/4] verified shot.png (2 hidden, 1.4s)
[3/4] verified sub/second.mov (1 hidden, 11.9s)
[4/4] verified sub/shot2.jpg (2 hidden, 1.4s)
4 file(s): 4 verified, 0 to review, 0 failed, 0 skipped (unchanged)
Report: /home/me/Videos/safe/report.json
```

What you get:

```
safe/
├── demo.mp4            masked and verified
├── shot.png
├── sub/second.mp4      videos come out as mp4
├── sub/shot2.jpg
├── _review/            only when something may still be readable: look before sharing
├── report.json
└── report.md
```

Options: `--lang vie+eng` (Vietnamese screens), `--kinds email,phone,api-key` (also `jwt`,
`private-key`, `credit-card`, `secret-assignment`, `url-credentials`, `high-entropy`), `--style mosaic`,
`--overwrite`, `--notify` (desktop notice), `--on-done "command"`, `--callback https://…`.
Keep a folder masked as files arrive: `agent-mask watch <in> <out>`.

## Use it from an agent

### MCP (Claude Code, Codex, Cursor…)

```bash
claude mcp add agent-mask -- agent-mask mcp
codex mcp add agent-mask -- agent-mask mcp
```

Then just ask: *"Mask every recording in ~/Videos/raw into ~/Videos/safe and tell me which ones need a look."*

| Tool | What it does |
|---|---|
| `mask_folder` | Starts a job for a folder; `wait: true` returns only when it is done |
| `mask_files` | The same for some files of a folder |
| `job_wait` | Blocks until the job ends, then returns the result: the simplest way for an agent to be told |
| `job_status` | Where a job is now (`files: true` lists every file) |
| `jobs_list`, `job_cancel` | List jobs and stop one after its current file |

While waiting, a client that sends a progress token receives one MCP progress notification per file.

### HTTP API

```bash
agent-mask serve            # http://127.0.0.1:7788, prints its token
```

```bash
curl -H "Authorization: Bearer $TOKEN" -X POST http://127.0.0.1:7788/jobs \
  -d '{"input":"/data/raw","output":"/data/safe","callbackUrl":"https://example.com/hook"}'
curl -H "Authorization: Bearer $TOKEN" -X POST "http://127.0.0.1:7788/jobs/<id>/wait?timeout=600"
curl -H "Authorization: Bearer $TOKEN" -N http://127.0.0.1:7788/jobs/<id>/events   # live progress (SSE)
```

| Endpoint | |
|---|---|
| `POST /jobs` | `{"input", "output", "files"?, "kinds"?, "lang"?, "style"?, "overwrite"?, "callbackUrl"?, "notify"?}` → 202 with the job (`"onDone"`, a shell command, only when the server was started with `--allow-commands`) |
| `GET /jobs`, `GET /jobs/{id}` | The jobs, one job with every file |
| `POST /jobs/{id}/wait?timeout=600` | Returns when the job ends |
| `GET /jobs/{id}/events` | Server-sent events: `started`, `file`, `done` |
| `POST /jobs/{id}/cancel` | Stops after the current file |
| `GET /health` | Version and status |

It listens on 127.0.0.1 only, every request needs the token, and requests sent by a web page
(with an `Origin` header) are refused, so a website you visit cannot drive it.

### When a job ends

- **Callback:** `callbackUrl` receives a POST `{"event":"job.ended","job":{…},"summary":"…"}` with
  `X-Agent-Mask-Signature: sha256=<HMAC-SHA256 of the body, keyed with your token>`, retried on errors.
- **Command** (from the command line; never from an agent's tool call): `--on-done "…"` runs with `AGENT_MASK_JOB`, `AGENT_MASK_STATUS`, `AGENT_MASK_REPORT`, `AGENT_MASK_OUTPUT`.
- **Desktop notice:** `--notify`.
- **Files:** `report.json` appears in the output folder when the job ends.

## Good to know

- Agent Mask reads **text** (OCR with Tesseract). Faces, voices, and text too small or too blurred
  to read are not hidden. When in doubt, the file goes to `_review/`: look at it before sharing.
- Speed depends on length and resolution: about real time for a screen recording on a 4-core
  laptop (a 12-second 720p clip takes about 12 seconds), about a second or two per screenshot.
- Everything runs locally with ffmpeg and Tesseract; no account, no telemetry, no upload.
- Agent Mask shares its detection engine with **Agent Recorder**, a screen recorder for tutorials
  that hides secrets while you record.

## License

MIT, see [LICENSE](LICENSE). ffmpeg and Tesseract are separate programs under their own licenses
and are not bundled.

---

## Tiếng Việt

### Agent Mask — thả cả thư mục video vào, nhận lại bản đã che sạch thông tin nhạy cảm

Miễn phí, mã nguồn mở (MIT), chạy hoàn toàn trên máy của bạn: không tải gì lên mạng.

Bạn vừa quay hai mươi video hướng dẫn, một buổi demo hay một video báo lỗi, và đâu đó lướt qua
file `.env`, email của khách hàng ở thanh bên, hay một token hiện trong terminal nửa giây. Che tay
là phải soát từng khung hình, sót một khung là lộ.

Agent Mask đọc chữ trên màn hình theo từng khung hình, tìm những gì không được chia sẻ, che lại
**kể cả khi chữ đang chạy** (cuộn trang, gõ phím, kéo cửa sổ), rồi **tự đọc lại kết quả của
chính mình**. File chỉ được đánh dấu *đã xác nhận* khi lần đọc lại không thấy gì. File còn nghi
ngờ được để riêng vào thư mục `_review/`, nên không thể lỡ tay chia sẻ.

**Tính năng chính**

- **Cả thư mục một lần:** video (mp4, mov, mkv, webm, avi, m4v, wmv) và ảnh (png, jpg, webp, bmp,
  tif), giữ nguyên cấu trúc thư mục con.
- **Tìm đúng thứ cần che:** email, số điện thoại, khoá API và token (OpenAI, Anthropic, AWS, GitHub,
  GitLab, Google, Slack, Stripe, npm…), JWT, khoá riêng, mật khẩu dạng `PASSWORD=…`, mật khẩu trong đường link, số thẻ,
  chuỗi ngẫu nhiên dài; cả tiếng Anh lẫn tiếng Việt.
- **Che đẹp, bám theo chữ:** bí mật phủ màu nền phẳng, thông tin cá nhân làm mờ ô vuông.
- **Tự kiểm tra:** mọi file kết quả đều được quét lại. "Đã xác nhận" là phải đạt, không mặc định.
- **Báo cáo rõ ràng:** `report.json` cho máy, `report.md` cho người: từng file, kết quả, che bao
  nhiêu chỗ, loại gì. Không bao giờ ghi lại nội dung đã che.
- **Chạy lại không làm lại:** file không đổi được bỏ qua trong tích tắc.
- **Canh thư mục:** `agent-mask watch` — thả file vào là được che ngay.
- **Sinh ra cho agent:** máy chủ MCP cho Claude Code, Codex, Cursor…; API trên máy; báo khi xong
  bằng webhook có chữ ký, lệnh tuỳ chọn hoặc thông báo trên màn hình.

**Bắt đầu nhanh**

1. Windows: chạy `AgentMask-Setup-<phiên bản>.exe` trong [Releases](https://github.com/adamwang99/agent-mask/releases), bộ cài sẽ hỏi để cài ffmpeg và Tesseract giúp bạn. macOS: `brew install ffmpeg tesseract`. Linux: `sudo apt install ffmpeg tesseract-ocr`.
2. Kiểm tra: `agent-mask doctor`
3. Chạy: `agent-mask batch <thư mục vào> <thư mục ra> --lang vie+eng`

**Dùng với agent:** `claude mcp add agent-mask -- agent-mask mcp`, rồi nói với agent:
*"Che thông tin nhạy cảm trong mọi video ở thư mục raw, xuất ra thư mục safe, báo tôi file nào cần xem lại."*
Agent gọi `mask_folder` rồi `job_wait`, và nhận kết quả ngay khi xong.

**Lưu ý:** Agent Mask che **chữ** đọc được trên hình. Khuôn mặt, giọng nói, chữ quá nhỏ hoặc quá
mờ thì không che. File còn nghi ngờ nằm trong `_review/`: hãy xem trước khi chia sẻ.

Giấy phép MIT. Agent Mask dùng chung bộ máy nhận diện với **Agent Recorder** — ứng dụng quay màn
hình làm video hướng dẫn, tự che bí mật ngay khi quay.
