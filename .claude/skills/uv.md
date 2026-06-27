---
name: uv
description: สร้างและจัดการโปรเจ็ค Python ด้วย uv workspace โดยมีโครงสร้างแบบแยกแพ็คเกจ core/connectors และใช้ src-layout
---

# UV Workspaces with src-layout and Shared Core Packages

สกิลสำหรับใช้ในการจัดโครงสร้าง Python Project ด้วย `uv` workspace ที่มีส่วนประกอบดังนี้:
1. แยกส่วนที่เป็น Core / Library / Connectors ออกมาเป็นแพ็คเกจต่างหาก (แยก subdirectory)
2. ใช้โครงสร้างโฟลเดอร์แบบ `src` layout (`src/<package_name>/`) เพื่อให้ได้โครงสร้างแพ็คเกจที่สะอาดและได้มาตรฐาน
3. บริหารจัดการ dependency ร่วมกันผ่าน `uv` workspace และ `uv.lock` เดียวกันที่ root

---

## โครงสร้างโปรเจ็คที่แนะนำ (Project Structure Layout)

```text
my-project/
├── pyproject.toml         # Root workspace config (ไม่ระบุ project metadata)
├── uv.lock                # Shared lockfile สำหรับทุก packages
├── connectors/            # Shared Library (เช่น connectors, core, utils)
│   ├── pyproject.toml     # ใช้ hatchling build system และ src-layout
│   └── src/
│       └── connectors/
│           ├── __init__.py
│           └── rabbitmq.py
├── producer/              # Service A (เช่น FastAPI Web API)
│   ├── pyproject.toml     # ใช้ hatchling + reference ไปยัง connectors
│   └── src/
│       └── producer_service/
│           └── main.py
└── consumer/              # Service B (เช่น Celery/Pika Worker)
    ├── pyproject.toml     # ใช้ hatchling + reference ไปยัง connectors
    └── src/
        └── consumer_service/
            └── main.py
```

---

## 1. การตั้งค่า pyproject.toml ในแต่ละระดับ

### A. Root `pyproject.toml`
ที่ root ของโปรเจ็คจะไม่มีโค้ดและไม่ต้องระบุ metadata ของโครงการ แต่จะมีหน้าที่หลักในการรวม workspace members ทั้งหมด:

```toml
[tool.uv.workspace]
members = ["producer", "consumer", "connectors"]
```

> [!NOTE]
> การใส่ `connectors` เข้ามาใน `members` จะช่วยให้ `uv sync` รันการติดตั้ง/อัปเดต dependency ของ connectors ไปพร้อมๆ กัน และสามารถพัฒนาแบบ dynamic editable ได้ดีขึ้น

---

### B. Shared Library (`connectors/pyproject.toml`)
สำหรับไลบรารีส่วนกลางที่ทุก services จะเรียกใช้:

```toml
[project]
name = "connectors"
version = "0.1.0"
description = "Shared connectors for RabbitMQ and other infrastructure"
readme = "README.md"
requires-python = ">=3.10"
dependencies = [
    "pika>=1.3.0",
]

[build-system]
requires = ["hatchling"]
build-backend = "hatchling.build"

[tool.hatch.build.targets.wheel]
packages = ["src/connectors"]
```

> [!IMPORTANT]
> 1. ต้องใช้ `hatchling` เป็น build-backend
> 2. ในส่วน `[tool.hatch.build.targets.wheel]` ต้องระบุ `packages = ["src/connectors"]` เพื่อบอกให้ hatch ค้นหาโค้ดแพ็คเกจที่อยู่ใต้โฟลเดอร์ `src/`

---

### C. Service Package (เช่น `producer/pyproject.toml`)
สำหรับแอพพลิเคชันปลายทางที่ต้องการ import ไลบรารีส่วนกลางไปใช้งาน:

```toml
[project]
name = "producer"
version = "0.1.0"
description = "Producer service using FastAPI"
readme = "README.md"
requires-python = ">=3.10"
dependencies = [
    "fastapi>=0.100.0",
    "uvicorn[standard]>=0.22.0",
    "connectors",  # 1. ระบุชื่อแพ็คเกจของ shared library ที่นี่
]

[tool.uv.sources]
connectors = { path = "../connectors" }  # 2. ชี้ตำแหน่งไปยังโฟลเดอร์แบบ local path

[build-system]
requires = ["hatchling"]
build-backend = "hatchling.build"

[tool.hatch.build.targets.wheel]
packages = ["src/producer_service"]
```

---

## 2. Command Cheatsheet สำหรับการพัฒนา (Development Workflow)

### การเตรียมการ (Setup & Sync)
หลังจากสร้างไฟล์โครงสร้างเสร็จสิ้น ให้เปิด terminal ที่ root directory แล้วรันคำสั่ง:
```bash
# ติดตั้ง dependencies ทั้งหมดใน workspace ลงใน .venv เดียวกัน
uv sync
```
*ระบบจะดาวน์โหลด library ภายนอกและทำการลิงก์ `connectors` และ `producer`/`consumer` เข้าสู่ `.venv` ในรูปแบบ **Editable mode** ทันที ทำให้เวลาแก้โค้ดใน `connectors/` บริการอื่นที่เรียกใช้จะได้รับผลการอัปเดตแบบเรียลไทม์*

### การเพิ่ม/ลบ Dependencies ในโปรเจ็คย่อย
เมื่อต้องการเพิ่มแพ็คเกจภายนอก ให้ใช้แฟล็ก `--package` เพื่อเจาะจงแพ็คเกจใน workspace:
```bash
# เพิ่ม httpx ไปที่ producer service เท่านั้น
uv add --package producer httpx

# เพิ่ม redis ไปที่ connectors library เท่านั้น
uv add --package connectors redis

# อัปเดต lockfile และ sync สภาพแวดล้อม
uv sync
```

### การสั่งรันคำสั่งแยกแต่ละ package
สามารถรันโปรแกรมผ่าน `uv run -p` หรือรันผ่าน path ของแต่ละโฟลเดอร์ได้:
```bash
# รัน FastAPI จาก root โดยใช้ context ของ producer
uv run -p producer uvicorn producer_service.main:app --host 0.0.0.0 --port 8000 --reload
```

---

## 3. วิธีการอ้างอิงและการเรียกใช้ในโค้ด (Importing in Code)

ในส่วนของ `producer/src/producer_service/main.py` คุณสามารถเรียกใช้งาน `connectors` ได้โดยตรงเสมือนเป็น library ที่ติดตั้งจาก PyPI:

```python
from fastapi import FastAPI
from connectors.rabbitmq import RabbitMQConnector  # เรียกใช้ shared library

app = FastAPI()
rabbitmq = RabbitMQConnector()

@app.get("/")
def read_root():
    return {"status": "connected", "rabbitmq_host": rabbitmq.host}
```

---

## 4. ข้อพิจารณาเพิ่มเติม (Best Practices)

- **การแยก Platform-Specific Dependencies**: 
  หากมีการดึง PyTorch หรือ library ที่ต้องแยก index สำหรับ GPU/CPU (เช่น CUDA บน Linux และ CPU บน macOS) ให้ใช้ Marker ใน `pyproject.toml` ของแพ็คเกจที่ใช้งาน เช่น:
  ```toml
  [tool.uv.sources]
  torch = [
    { index = "pytorch-cu128", marker = "sys_platform == 'linux'" },
    { index = "pytorch-cpu", marker = "sys_platform == 'darwin'" }
  ]
  ```
- **การระบุ Dockerfile**:
  ในการ build image ให้ copy โฟลเดอร์ `connectors` และ service โฟลเดอร์ที่ต้องการเข้าไปพร้อมกัน แล้วรัน `uv pip install --system -e ./connectors -e ./producer` เพื่อลงทะเบียนแพ็คเกจในระดับ system/container
