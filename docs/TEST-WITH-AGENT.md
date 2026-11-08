# Thử Agent Mask bằng agent

Tài liệu cho agent tester (Claude Code, Codex, Cursor…) chạy trên máy thật: Windows 10/11 (chính),
macOS hoặc Linux nếu có. Chỉ dùng **dữ liệu giả**: không đưa bất kỳ mật khẩu, khoá hay email thật nào vào thư mục thử.

## Chuẩn bị (người làm, trước khi giao cho agent)

1. Cài Agent Mask, chọn một trong hai cách:
   - Windows: chạy `AgentMask-Setup-0.1.0.exe`. Bộ cài chưa ký số, SmartScreen có thể hỏi: bấm **More info → Run anyway**. Ở trang cuối, để tick "Check and install ffmpeg and Tesseract" và trả lời Y.
   - Có Go 1.24+: `go install github.com/adamwang99/agent-mask/cmd/agent-mask@latest`, rồi tự cài ffmpeg và Tesseract (Windows: `winget install Gyan.FFmpeg` và `winget install UB-Mannheim.TesseractOCR`; macOS: `brew install ffmpeg tesseract`; Linux: `sudo apt install ffmpeg tesseract-ocr`).
2. Nối MCP cho agent:
   - Claude Code: `claude mcp add agent-mask -- agent-mask mcp`
   - Codex: `codex mcp add agent-mask -- agent-mask mcp`
   - (Nếu `agent-mask` chưa có trong PATH, thay bằng đường dẫn đầy đủ, ví dụ `"%LOCALAPPDATA%\Programs\Agent Mask\agent-mask.exe"`.)
3. Mở **terminal mới** và **phiên agent mới** (agent chỉ nạp MCP khi bắt đầu phiên).

## Dán đoạn sau cho agent

---

Hãy kiểm thử ứng dụng Agent Mask trên máy này và viết báo cáo. Quy tắc: chỉ dùng dữ liệu giả do bạn tự tạo; không sửa file của ứng dụng; làm việc trong một thư mục thử riêng (Windows: `%USERPROFILE%\am-test`, macOS/Linux: `~/am-test`); chép nguyên văn mọi lỗi.

**0. Môi trường**
- Chạy `agent-mask version` và `agent-mask doctor`, chép kết quả. Nếu doctor báo MISSING hoặc BROKEN, dừng lại và báo tôi.
- Ghi lại: hệ điều hành và phiên bản, CPU, RAM.

**1. Tạo dữ liệu thử bằng ffmpeg** trong `am-test/in/` (có thư mục con `sub/` và `tên có dấu/`):
- Tạo file `secrets.txt` gồm các dòng giả:
  ```
  Contact: jane.doe@example.com
  API_KEY=sk-proj-AbC123xYz789QwErTy456UiOp
  DB_PASSWORD=Tr0ngDong2026x
  Phone: 0912 345 678
  Card: 4111 1111 1111 1111
  Settings page - nothing secret on this line
  ```
- Tạo các file sau (dùng `drawtext` với `textfile=secrets.txt`, chữ đen trên nền trắng, cỡ 28–36). Trên Windows nếu drawtext báo lỗi font, thêm `fontfile='C\:/Windows/Fonts/arial.ttf'`; macOS dùng `/System/Library/Fonts/Supplemental/Arial.ttf`.
  1. `in/shot.png`: ảnh 1280x720.
  2. `in/sub/shot2.jpg`: cùng nội dung, dạng jpg.
  3. `in/tên có dấu/ảnh thử.png`: cùng nội dung (kiểm tra tên file tiếng Việt và dấu cách).
  4. `in/clean.png`: ảnh chỉ có dòng "Settings page - nothing secret on this line".
  5. `in/scroll.mp4`: video 15 giây 1280x720, 30 fps, chữ **cuộn dần lên** (ví dụ `y=h-t*60`).
  6. `in/sub/static.mov`: video 10 giây, chữ đứng yên, dạng mov.
  7. `in/notes.txt`: file văn bản thường (phải bị bỏ qua).
- Liệt kê lại cây thư mục `in/`.

**2. Xử lý hàng loạt bằng dòng lệnh**
- Chạy `agent-mask batch am-test/in am-test/out` và đo thời gian. Chép toàn bộ đầu ra và mã thoát.
- Kiểm tra:
  - `out/` có đủ 6 file media với thư mục con giữ nguyên; video ra đuôi `.mp4`.
  - `notes.txt` không được xử lý.
  - Có `report.json` và `report.md`.
- Mở `report.md`, chép lại bảng kết quả.
- Tìm trong `report.json` và `report.md` các chuỗi `jane.doe`, `sk-proj`, `Tr0ngDong`, `345 678`, `4111`. Phải **không** tìm thấy chuỗi nào.
- Trích 3 khung hình của `out/scroll.mp4` (giây 2, 7, 12) bằng ffmpeg, cùng `out/shot.png` và `out/sub/shot2.jpg`. Xem từng ảnh và trả lời:
  - Email, khoá API, mật khẩu, số điện thoại, số thẻ có bị che không?
  - Mắt người còn đoán ra chữ nào không?
  - Vùng che có bám theo chữ khi cuộn không?
  - Dòng "Settings page…" có còn đọc được (không bị che oan)?
- `clean.png` cho kết quả gì?
- Nếu có file trong `out/_review/`, ghi rõ file nào và lý do trong báo cáo.

**3. Chạy lại và các trường hợp biên**
- Chạy lại đúng lệnh ở bước 2: mọi file phải là `skipped`, xong trong vài giây.
- Chạy lại với `--overwrite`: mọi file được làm lại.
- `agent-mask batch am-test/in am-test/in/out2`: phải báo lỗi (thư mục ra nằm trong thư mục vào).
- `agent-mask batch am-test/khong-ton-tai am-test/out3`: phải báo lỗi.
- `agent-mask batch am-test/in am-test/out4 --kinds email`: chỉ email bị che, khoá và mật khẩu còn nguyên. Xem ảnh để xác nhận.
- `agent-mask batch am-test/in am-test/out5 --style solid` và `--style blur`: so sánh cách che.
- Nếu Tesseract có tiếng Việt: tạo ảnh có dòng `Mật khẩu: Bimat2026x`, chạy với `--lang vie+eng`, xem có bị che không.

**4. Báo khi xong**
- `agent-mask batch am-test/in am-test/out6 --notify --on-done "<lệnh ghi file am-test/done.txt chứa %AGENT_MASK_STATUS% hoặc $AGENT_MASK_STATUS>"`.
- Kiểm tra: `done.txt` có nội dung `done`. Có thông báo hiện ở góc màn hình không? Nếu có, chụp màn hình.

**5. MCP** (dùng các tool `agent-mask` trong phiên này)
- Liệt kê các tool thấy được. Phải có: `mask_folder`, `mask_files`, `job_wait`, `job_status`, `jobs_list`, `job_cancel`.
- Gọi `mask_folder` với input `am-test/in`, output `am-test/out-mcp` (không `wait`). Ghi lại id trả về ngay.
- Gọi `job_status`, rồi `job_wait` với id đó. Chép kết quả.
  - Bạn có nhận được báo tiến độ từng file trong lúc chờ không?
- Gọi `mask_folder` lần nữa với `wait: true`, output `am-test/out-mcp2`.
- Gọi `mask_files` với `files: ["shot.png", "sub/static.mov"]`, rồi `job_wait`.
- Gọi `job_cancel` ngay sau khi giao một việc mới với `overwrite: true`. Việc đó phải dừng sau file đang làm, trạng thái `canceled`.
- Gọi `jobs_list`.

**6. API trên máy**
- Mở một terminal riêng chạy `agent-mask serve`, ghi lại cổng và token in ra.
- Viết một máy chủ nhận webhook nhỏ trên máy (Python hoặc PowerShell, cổng 8899). Nó ghi lại body và header `X-Agent-Mask-Signature` của mỗi POST, trả về 200.
- Gọi API bằng curl hoặc `Invoke-RestMethod`:
  - `GET /health` không có token: phải là 401.
  - `GET /health` có token nhưng thêm header `Origin: https://example.com`: phải là 403.
  - `POST /jobs` với body:
    ```
    {"input":"<đường dẫn am-test/in>","output":"<đường dẫn am-test/out-api>","callbackUrl":"http://127.0.0.1:8899/hook"}
    ```
    Phải trả về 202 và có id.
  - `GET /jobs/<id>/events`: chép các sự kiện nhận được (`file`, `done`).
  - `POST /jobs/<id>/wait?timeout=600`: chép kết quả.
  - Máy chủ webhook có nhận được POST không? Tính HMAC-SHA256 của body với khoá là token, so với header `X-Agent-Mask-Signature` (dạng `sha256=<hex>`): có khớp không?
  - `GET /jobs` và `POST /jobs/<id>/cancel`.
- Dừng `serve` bằng Ctrl+C.

**7. Canh thư mục**
- Chạy `agent-mask watch am-test/drop am-test/drop-out` (tạo `drop` trước) ở terminal riêng.
- Chép `shot.png` vào `drop/` và đợi khoảng 15 giây: file đã che phải xuất hiện trong `drop-out/`.
- Chép tiếp một video: cũng phải được xử lý.
- Dừng bằng Ctrl+C.

**8. Thử với video thật (nếu có)**
- Lấy 1–3 bản quay màn hình thật dài 1–5 phút, có hiện dữ liệu giả (ví dụ mở Notepad gõ các dòng trong `secrets.txt` rồi quay), cho vào `am-test/real/`.
- Chạy `batch`. Ghi thời gian xử lý mỗi file so với độ dài video, và kết quả `verified` hay `_review`.
- Xem lại video ra: có khung hình nào lộ chữ không? Ghi giây cụ thể.

**Báo cáo** — một bảng tổng hợp mỗi bước (Đạt / Không đạt / Không thử được, kèm lý do), rồi:
- thông tin máy và kết quả `doctor`;
- thời gian xử lý từng loại file;
- mọi chỗ còn lộ hoặc bị che oan (file, giây, nội dung lộ được mô tả chung, không chép dữ liệu);
- các lỗi nguyên văn;
- góp ý về câu chữ thông báo, báo cáo và README nếu thấy khó hiểu.

---
