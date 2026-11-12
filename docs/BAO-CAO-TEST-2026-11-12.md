# Báo cáo test 0.1.3 — video cuộn nhanh / nhảy ngược (máy Windows thật)

Tester: Atom (agent tester) · 12/11/2026 · Windows 11 10.0.22631 · Xeon E5-2680 v4 (28 luồng) · 64 GB
Bản test: `git clone` @ `94319b4` (0.1.3), build `go build ./cmd/agent-mask`, `agent-mask version` → `0.1.3`.

## Dữ liệu test

`docs/test-data/training-demo.mp4` — 1920x1080, 59,1 s, 30 fps, **1773 khung**, dựng bằng ffmpeg: trang wiki 13 mục,
**8 bí mật giả** rải khắp trang, 12 đoạn tốc độ 0 / 45 / 220 / 15 / 60 / 120 / 35 / **250** px/s và **hai cú nhảy ngược**
(1200 px trong 0,6 s, và 1500 px trong 0,5 s).

Bí mật (đều giả): `jane.doe@example.com`, `ops@example.org`, `sk-proj-AbC123xYz789QwErTy456UiOp`, `Tr0ngDong2026x`,
`0912 345 678`, `4111 1111 1111 1111`, `Mail2026secret`, `ghp_Ab12Cd34Ef56Gh78Ij90Kl12Mn34Op56Qr78`.

## Kết quả 0.1.3

```
[1/1] review   training-demo.mp4 (26 hidden, 91.3s) -> _review/ (16 sensitive region(s) still readable)
```

Đo độc lập: trích **từng khung** (`-fps_mode passthrough`, 30 khung/s) rồi OCR bằng Tesseract, không dùng bộ tự kiểm.

**22 / 1773 khung còn đọc được bí mật** (`docs/test-data/scan-0.1.3.txt`):

| Giây | Khung | Lộ gì | Bối cảnh |
|---|---|---|---|
| 11,87 – 11,93 | 3 | email | dòng chữ đi vào từ **mép dưới** |
| 22,10 – 22,17 | 3 | email | ngay sau **cú nhảy ngược thứ nhất** |
| 50,07 – 52,20 | 16 | `DB_PASSWORD`, `API_KEY` | đoạn **cuộn 250 px/s** |

Ảnh cắt từ bản đã che: `docs/test-data/leak-1190.png` (giây 11,9), `docs/test-data/leak-5120.png` (giây 51,2 —
API key **và** mật khẩu DB đọc nguyên văn, các hộp che nằm lệch hẳn sang dòng khác/khoảng trắng).

Tiến bộ so với các bản trước trên cùng video này:

| Bản | Khung lộ | Ghi chú |
|---|---|---|
| 0.1.0 | ~22 | |
| 0.1.1 | **147** | có cửa sổ 4,6 s liên tục ở 9,5–14,6 s |
| 0.1.3 | **22** | cửa sổ dài đã hết, còn 3 cửa sổ ngắn |

## Hai điểm cần coder xử lý

**A. Còn lộ khi cuộn nhanh (250 px/s) và ngay sau cú nhảy.** Hộp che lệch khoảng **một dòng (~46 px)**, tức offset đang cũ
hơn khung đang vẽ chừng 5–6 khung ở tốc độ 250 px/s. Đề nghị: đo độ trôi cho **từng khung** (hoặc bù offset theo
chênh lệch giữa khung đọc và khung vẽ) chứ không chỉ "alongside the reads"; và khi phát hiện nhảy (chênh > một ngưỡng)
thì đọc lại ngay khung đó.

Nghiệm thu: chạy đúng `training-demo.mp4` kèm theo → **0 khung** OCR còn `jane.doe@example.com`, `sk-proj`,
`Tr0ngDong`, `0912 345 678`, `4111 1111 1111 1111`; và `agent-mask verify`-tương đương trả `verified`.

**B. `regions` trong `report.json` ghi `startSec: 0, endSec: 59.1` cho cả 26 vùng**, kể cả vùng chỉ nằm trong khung vài
giây. Nếu trường này để nói "khi nào bí mật ở trên màn hình" thì nó sai; nếu là "khoảng đã đọc" thì nên đổi tên trong README.
Ngoài ra bộ tự kiểm báo **16 vùng còn đọc được** trong khi OCR độc lập chỉ thấy lộ ở 3 cửa sổ ⇒ nên in **toạ độ + giây**
của từng vùng bị báo (vào `report.json`, không in nội dung) để người dùng đối chiếu, thay vì chỉ một con số.

## Tệp kèm

- `docs/test-data/training-demo.mp4` — video test (dữ liệu giả)
- `docs/test-data/scan-0.1.3.txt` — danh sách đầy đủ các khung lộ (giây + loại)
- `docs/test-data/leak-1190.png`, `docs/test-data/leak-5120.png` — ảnh cắt bằng chứng
- `docs/test-data/report-0.1.3.json` — report của lần chạy
