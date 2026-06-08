"""
Render the critical pages of 'Swing Trading Simplified' as high-res PNG images
so they can be read directly via Claude's vision capability.

Targets:
- Chapter summary pages (last page of each chapter - garbled by Windows OCR)
- Risk/sizing tables (R-multiples, drawdown recovery, win-rate probabilities)
- Bird's-eye scanner spec pages (Ch 11 scan boxes)
- Chapter title pages
"""

import fitz
import os

PDF_PATH = r"C:\Users\Admin\Downloads\swing trading simplified.pdf"
OUT_DIR = r"C:\Projects\bnf_go_engine\book_pages"
os.makedirs(OUT_DIR, exist_ok=True)

# Critical pages - by PDF page number (1-indexed for clarity, but PyMuPDF uses 0-indexed)
CRITICAL_PAGES = {
    # Contents page
    3: "contents",
    4: "contents_2",
    # Chapter title pages + key concept intros
    9: "ch1_focus_winning_edge_start",
    24: "ch1_summary",
    25: "ch2_concepts_drive_markets_start",
    37: "ch2_summary",
    38: "ch3_momentum_start",
    43: "ch3_trigger_candle_definition",
    49: "ch3_ema_role",
    72: "ch3_summary",
    73: "ch4_perfect_setup_start",
    76: "ch4_vcp_footprint",
    151: "ch4_summary",
    152: "ch5_buying_precision_start",
    153: "ch5_pivot_diagram",
    167: "ch5_summary",
    168: "ch6_timing_exits_start",
    170: "ch6_r_gains_table",
    184: "ch6_summary",
    185: "ch7_survive_thrive_start",
    186: "ch7_stop_loss_methods",
    190: "ch7_risk_rules",
    194: "ch7_tight_vs_wide_stops",
    200: "ch7_summary",
    201: "ch8_position_sizing_start",
    205: "ch8_break_even_winrate_table",
    206: "ch8_expectancy_formula",
    212: "ch8_drawdown_chart",
    213: "ch8_drawdown_recovery_table",
    214: "ch8_risk_per_trade_impact",
    216: "ch8_finding_balance",
    220: "ch8_summary",
    224: "ch8_summary_v2",
    225: "ch9_mind_over_money_start",
    226: "ch9_four_fears",
    231: "ch9_winners_required_table",
    234: "ch9_pyramiding",
    245: "ch9_journal_template",
    255: "ch9_summary",
    256: "ch10_market_aware_start",
    260: "ch10_10_20_rule",
    262: "ch10_market_breadth",
    264: "ch10_net_new_highs",
    272: "ch10_summary",
    273: "ch11_scanning_start",
    275: "ch11_ema_scan_box",
    276: "ch11_weekly_scan",
    277: "ch11_monthly_gainers_scan",
    278: "ch11_tight_range_scan_52w_high",
    279: "ch11_tight_range_scan_v2",
    280: "ch11_trigger_candle_scan",
    283: "ch11_watchlist_filter",
    284: "ch11_watchlist_org",
    287: "ch11_summary",
    288: "ch12_bringing_together_start",
    297: "ch12_effortless_trading",
    298: "ch12_closing_note",
}

def render_page(pdf_path, page_num_1indexed, out_path, zoom=3.0):
    """Render a page at high resolution for vision OCR."""
    doc = fitz.open(pdf_path)
    page = doc[page_num_1indexed - 1]  # convert to 0-indexed
    mat = fitz.Matrix(zoom, zoom)
    pix = page.get_pixmap(matrix=mat)
    pix.save(out_path)
    doc.close()

def main():
    print(f"Rendering {len(CRITICAL_PAGES)} critical pages at 3x zoom...")
    for pg_num, label in sorted(CRITICAL_PAGES.items()):
        out_file = os.path.join(OUT_DIR, f"p{pg_num:03d}_{label}.png")
        render_page(PDF_PATH, pg_num, out_file)
        print(f"  p{pg_num:03d} -> {label}.png")
    print(f"\nDone! Files in: {OUT_DIR}")

if __name__ == '__main__':
    main()
