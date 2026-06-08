import sys, fitz
pdf_path = sys.argv[1]
page_num = int(sys.argv[2])
out_path = sys.argv[3]
doc = fitz.open(pdf_path)
page = doc[page_num]
mat = fitz.Matrix(2.5, 2.5)  # High res for better OCR
pix = page.get_pixmap(matrix=mat)
pix.save(out_path)
doc.close()
