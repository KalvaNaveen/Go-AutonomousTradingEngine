# Windows OCR script for PDF pages
# Requires: pymupdf installed, Windows 10/11

param(
    [string]$PdfPath = "C:\Users\Admin\Downloads\swing trading simplified.pdf",
    [string]$OutputPath = "C:\Projects\bnf_go_engine\book_full_text.txt",
    [int]$StartPage = 0,
    [int]$EndPage = 305
)

Add-Type -AssemblyName System.Runtime.WindowsRuntime

# Helper to await WinRT async operations
$null = [Windows.Storage.StorageFile, Windows.Storage, ContentType=WindowsRuntime]
$null = [Windows.Media.Ocr.OcrEngine, Windows.Foundation, ContentType=WindowsRuntime]
$null = [Windows.Graphics.Imaging.SoftwareBitmap, Windows.Foundation, ContentType=WindowsRuntime]
$null = [Windows.Storage.Streams.RandomAccessStream, Windows.Storage.Streams, ContentType=WindowsRuntime]

# Await helper for WinRT async operations
function Await-Task($WinRtTask) {
    $asTask = [System.WindowsRuntimeSystemExtensions]::AsTask($WinRtTask)
    $asTask.Wait() | Out-Null
    return $asTask.Result
}

# Create OCR engine
$ocrEngine = [Windows.Media.Ocr.OcrEngine]::TryCreateFromUserProfileLanguages()
if ($null -eq $ocrEngine) {
    Write-Error "Could not create OCR engine"
    exit 1
}
Write-Host "OCR Engine created. Language: $($ocrEngine.RecognizerLanguage.DisplayName)"

# Create temp directory for page images
$tempDir = "C:\Projects\bnf_go_engine\ocr_temp"
if (-not (Test-Path $tempDir)) { New-Item -ItemType Directory -Path $tempDir -Force | Out-Null }

# Python script to render a single page to PNG
$renderScript = @'
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
'@

$renderScriptPath = "C:\Projects\bnf_go_engine\render_page.py"
$renderScript | Out-File -FilePath $renderScriptPath -Encoding utf8

# Process each page
$allText = [System.Text.StringBuilder]::new()

Write-Host "Processing pages $StartPage to $EndPage..."

for ($pageNum = $StartPage; $pageNum -le $EndPage; $pageNum++) {
    $imgPath = "$tempDir\page_$pageNum.bmp"

    # Render PDF page to image
    $result = python $renderScriptPath $PdfPath $pageNum $imgPath 2>&1

    if (-not (Test-Path $imgPath)) {
        Write-Warning "Failed to render page $pageNum"
        continue
    }

    try {
        # Load image as SoftwareBitmap
        $imgBytes = [System.IO.File]::ReadAllBytes($imgPath)
        $stream = [System.IO.MemoryStream]::new($imgBytes)
        $winrtStream = [System.Runtime.InteropServices.WindowsRuntime.WindowsRuntimeStreamExtensions]::AsRandomAccessStream($stream)

        $decoder = Await-Task([Windows.Graphics.Imaging.BitmapDecoder]::CreateAsync($winrtStream))
        $softwareBitmap = Await-Task($decoder.GetSoftwareBitmapAsync())

        # Convert to BGRA8 if needed
        if ($softwareBitmap.BitmapPixelFormat -ne [Windows.Graphics.Imaging.BitmapPixelFormat]::Bgra8) {
            $softwareBitmap = [Windows.Graphics.Imaging.SoftwareBitmap]::Convert($softwareBitmap, [Windows.Graphics.Imaging.BitmapPixelFormat]::Bgra8)
        }

        # OCR the bitmap
        $ocrResult = Await-Task($ocrEngine.RecognizeAsync($softwareBitmap))
        $pageText = $ocrResult.Text

        if ($pageText.Trim().Length -gt 0) {
            $null = $allText.AppendLine("=== PAGE $($pageNum + 1) ===")
            $null = $allText.AppendLine($pageText)
            $null = $allText.AppendLine()
        }

        # Clean up temp image
        Remove-Item $imgPath -Force -ErrorAction SilentlyContinue

        if (($pageNum + 1) % 10 -eq 0) {
            Write-Host "Completed page $($pageNum + 1) of $($EndPage + 1)..."
            # Save progress incrementally
            $allText.ToString() | Out-File -FilePath $OutputPath -Encoding utf8
        }
    }
    catch {
        Write-Warning "OCR error on page $pageNum: $_"
    }
}

# Final save
$allText.ToString() | Out-File -FilePath $OutputPath -Encoding utf8
Write-Host "Done! Output saved to $OutputPath"
Write-Host "Total characters: $($allText.Length)"
