"""
OCR all 306 pages of 'Swing Trading Simplified' using Windows OCR (winocr)
Renders each page via PyMuPDF, then OCRs via Windows built-in OCR engine.
"""

import fitz          # PyMuPDF
import winocr
from PIL import Image
import io
import os
import sys
import time

PDF_PATH = r"C:\Users\Admin\Downloads\swing trading simplified.pdf"
OUTPUT_PATH = r"C:\Projects\bnf_go_engine\book_full_text.txt"
PROGRESS_PATH = r"C:\Projects\bnf_go_engine\ocr_progress.txt"

def render_page_to_pil(doc, page_num, zoom=2.5):
    """Render a PDF page to a PIL Image."""
    page = doc[page_num]
    mat = fitz.Matrix(zoom, zoom)
    pix = page.get_pixmap(matrix=mat)
    img_bytes = pix.tobytes("png")
    return Image.open(io.BytesIO(img_bytes))

def ocr_page(pil_img):
    """Run Windows OCR on a PIL image."""
    result = winocr.recognize_pil_sync(pil_img, 'en')
    return result.get('text', '')

def main():
    doc = fitz.open(PDF_PATH)
    total = len(doc)
    print(f"Total pages: {total}", flush=True)

    all_text = []
    start_time = time.time()

    for page_num in range(total):
        try:
            img = render_page_to_pil(doc, page_num)
            text = ocr_page(img)

            page_block = f"=== PAGE {page_num + 1} ===\n{text}\n\n"
            all_text.append(page_block)

            elapsed = time.time() - start_time
            avg = elapsed / (page_num + 1)
            remaining = avg * (total - page_num - 1)

            if (page_num + 1) % 5 == 0 or page_num < 5:
                print(f"Page {page_num+1}/{total} | chars: {len(text)} | ETA: {remaining/60:.1f}min", flush=True)
                # Incremental save every 5 pages
                with open(OUTPUT_PATH, 'w', encoding='utf-8') as f:
                    f.write(''.join(all_text))
                with open(PROGRESS_PATH, 'w') as f:
                    f.write(f"{page_num+1}/{total}")

        except Exception as e:
            print(f"ERROR page {page_num+1}: {e}", flush=True)
            all_text.append(f"=== PAGE {page_num + 1} [OCR ERROR] ===\n\n")

    # Final save
    with open(OUTPUT_PATH, 'w', encoding='utf-8') as f:
        f.write(''.join(all_text))

    doc.close()
    total_time = time.time() - start_time
    print(f"\nDone! {total} pages in {total_time/60:.1f} minutes")
    print(f"Output: {OUTPUT_PATH}")
    print(f"Total chars: {sum(len(b) for b in all_text)}")

if __name__ == '__main__':
    main()
