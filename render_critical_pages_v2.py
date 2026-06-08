"""
Re-render critical pages with correct PDF page numbers (+8 offset from printed page).
"""

import fitz
import os

PDF_PATH = r"C:\Users\Admin\Downloads\swing trading simplified.pdf"
OUT_DIR = r"C:\Projects\bnf_go_engine\book_pages"
os.makedirs(OUT_DIR, exist_ok=True)

# Critical pages with corrected PDF page numbers (printed page + 8)
# Format: PDF_page: (printed_page, label)
CRITICAL_PAGES = {
    # Ch 7 - Risk Management
    194: (186, "ch7_volatility_atr_stops"),
    195: (187, "ch7_fixed_pct_stops"),
    196: (188, "ch7_lod_stops"),
    197: (189, "ch7_pdl_stop"),
    198: (190, "ch7_risk_rules_RULES"),
    199: (191, "ch7_risk_rules_cont"),
    208: (200, "ch7_summary_BOX"),

    # Ch 8 - Position Sizing
    213: (205, "ch8_break_even_table"),
    214: (206, "ch8_expectancy"),
    219: (211, "ch8_balance"),
    220: (212, "ch8_drawdown_concept"),
    221: (213, "ch8_drawdown_recovery_TABLE"),
    222: (214, "ch8_risk_per_trade_impact_TABLE"),
    223: (215, "ch8_drawdown_recovery_steps"),
    225: (216, "ch8_finding_balance_pos_sizing"),
    229: (220, "ch8_probability_consec_losses"),
    232: (224, "ch8_summary_BOX"),

    # Ch 9 - Mind Over Money
    234: (226, "ch9_four_fears"),
    241: (233, "ch9_break_loss_link"),
    264: (255, "ch9_summary_BOX"),

    # Ch 10 - Market Aware
    268: (260, "ch10_10_20_rule"),
    270: (262, "ch10_market_breadth"),
    272: (264, "ch10_net_new_highs"),
    279: (271, "ch10_situational_awareness"),
    281: (272, "ch10_summary_BOX"),

    # Other chapter summaries
    32: (24, "ch1_summary_BOX"),
    45: (37, "ch2_summary_BOX"),
    80: (72, "ch3_summary_BOX"),
    159: (151, "ch4_summary_BOX"),
    175: (167, "ch5_summary_BOX"),
    192: (184, "ch6_summary_BOX"),
    296: (287, "ch11_summary_BOX"),
}

def render_page(pdf_path, pdf_page_num, out_path, zoom=3.0):
    doc = fitz.open(pdf_path)
    page = doc[pdf_page_num - 1]  # 0-indexed
    mat = fitz.Matrix(zoom, zoom)
    pix = page.get_pixmap(matrix=mat)
    pix.save(out_path)
    doc.close()

def main():
    print(f"Rendering {len(CRITICAL_PAGES)} corrected critical pages at 3x zoom...")
    for pdf_pg, (printed_pg, label) in sorted(CRITICAL_PAGES.items()):
        out_file = os.path.join(OUT_DIR, f"pp{printed_pg:03d}_pdf{pdf_pg:03d}_{label}.png")
        render_page(PDF_PATH, pdf_pg, out_file)
        print(f"  printed p{printed_pg} (PDF p{pdf_pg}) -> {label}.png")
    print(f"\nDone! Files in: {OUT_DIR}")

if __name__ == '__main__':
    main()
