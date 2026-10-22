#!/usr/bin/env python3
"""Render local HTML via set_content and check page interactions; no navigation or business services."""
from __future__ import annotations
import argparse
import hashlib
import json
import shutil
from pathlib import Path


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument('--root', type=Path, default=Path(__file__).resolve().parents[1])
    parser.add_argument('--chromium', default=None)
    args = parser.parse_args()
    root = args.root.resolve()
    report_path = root / 'review/browser-checks.json'
    shots = root / 'review/screenshots'
    shots.mkdir(parents=True, exist_ok=True)
    report = {'scope': 'DOCUMENT_ONLY', 'document_version': 'v4.2.2',
              'spec_sha256': hashlib.sha256((root / 'spec.md').read_bytes()).hexdigest(),
              'status': 'NOT_RUN', 'checks': [], 'screenshots': [],
              'transport': 'local HTML loaded using Playwright set_content; no navigation',
              'navigation_status': 'NOT_RUN',
              'navigation_limit': 'This tool checks rendered content and in-page actions only. Link targets are checked statically; the generation environment blocked the earlier loopback navigation attempt.',
              'not_validated': ['business code', 'Fedora', 'BFF/Model', 'KFP/Trainer', 'Inference', 'GPU', 'cross-document navigation', 'OS clipboard contents']}
    def save() -> None:
        report_path.write_text(json.dumps(report, ensure_ascii=False, indent=2) + '\n', encoding='utf-8')
    def check(name: str, ok: bool, detail=None) -> None:
        report['checks'].append({'check': name, 'status': 'PASS' if ok else 'FAIL', 'detail': detail})
    try:
        from playwright.sync_api import sync_playwright
    except ImportError as exc:
        report['reason'] = str(exc)
        save()
        raise SystemExit('NOT_RUN: Playwright is not installed')
    executable = args.chromium or shutil.which('chromium') or shutil.which('chromium-browser')
    if not executable:
        report['reason'] = 'No approved Chromium executable found; installation not attempted.'
        save()
        raise SystemExit('NOT_RUN: Chromium not found')
    architecture = 'ANI-CPU受管微调-v4.2.2-业务与流程方案.html'
    development = 'ANI-CPU受管微调-v4.2.2-开发与Model对接说明.html'
    guide = 'ANI-CPU受管微调-v4.2.2-主流程开发与分段验收手册.html'
    errors = []
    try:
        with sync_playwright() as pw:
            browser = pw.chromium.launch(executable_path=executable, headless=True, args=['--no-sandbox'])
            ctx = browser.new_context(viewport={'width': 1440, 'height': 1000})
            # Documentation must not load assets or scripts from the Internet.
            external = []
            def guard(route):
                external.append(route.request.url)
                route.abort()
            ctx.route('**/*', guard)
            page = ctx.new_page()
            page.on('pageerror', lambda err: errors.append(str(err)))
            def load(filename: str, anchor: str | None = None) -> None:
                page.set_content((root / filename).read_text(encoding='utf-8'), wait_until='load')
                page.add_style_tag(content='html{scroll-behavior:auto!important}')
                page.evaluate('window.scrollTo({top:0,behavior:"instant"})')
                page.wait_for_timeout(50)
                if anchor:
                    page.locator('#' + anchor).evaluate('(el) => {const top=document.querySelector(".top").getBoundingClientRect().height;window.scrollTo({top:el.getBoundingClientRect().top+scrollY-top-16,behavior:"instant"});}')

            for filename in [p.name for p in sorted(root.glob('*.html'))]:
                for width, height, mode in [(1440, 1000, 'desktop'), (390, 844, 'mobile')]:
                    page.set_viewport_size({'width': width, 'height': height})
                    load(filename)
                    check(f'{mode}:content-loaded:{filename}', page.title() != '')
                    dimensions = page.evaluate('({scroll:document.documentElement.scrollWidth, width:innerWidth})')
                    check(f'{mode}:no-page-overflow:{filename}', dimensions['scroll'] <= dimensions['width'] + 1, dimensions)
                    check(f'{mode}:main-content:{filename}', page.locator('h1').count() == 1 and page.locator('main').inner_text().__len__() > 500)
            page.set_viewport_size({'width': 1440, 'height': 1000})
            load('index.html')
            page.screenshot(path=str(shots / 'index-desktop.png'), full_page=False)
            report['screenshots'].append('review/screenshots/index-desktop.png')
            # Cross-document navigation is intentionally not claimed here.
            load(development, 'D09-9-3')
            page.wait_for_timeout(100)
            check('dispatch-anchor', page.locator('#D09-9-3').count() == 1)
            page.screenshot(path=str(shots / 'dispatch-desktop.png'), full_page=False)
            report['screenshots'].append('review/screenshots/dispatch-desktop.png')
            load(guide, 'G03')
            page.wait_for_timeout(100)
            page.screenshot(path=str(shots / 'core-flow-desktop.png'), full_page=False)
            report['screenshots'].append('review/screenshots/core-flow-desktop.png')
            # Chapter filtering and reset.
            load(development)
            original_count = page.locator('.chapter[data-chapter]').count()
            page.locator('#search').fill('md-container')
            visible_count = page.locator('.chapter[data-chapter]:visible').count()
            check('chapter-filter', 0 < visible_count < original_count, {'matched': visible_count, 'total': original_count})
            page.locator('#search').fill('NO_MATCH_FOR_DOCUMENT_CHECK_73941')
            check('empty-search', page.locator('.chapter[data-chapter]:visible').count() == 0 and page.locator('#empty').is_visible())
            page.locator('#clear').click()
            check('clear-filter', page.locator('.chapter[data-chapter]:visible').count() == original_count)
            before = page.evaluate('getComputedStyle(document.documentElement).getPropertyValue("--font")')
            page.locator('#fontPlus').click()
            after = page.evaluate('getComputedStyle(document.documentElement).getPropertyValue("--font")')
            check('font-plus', float(after.removesuffix('px')) == float(before.removesuffix('px')) + 1)
            page.locator('#fontMinus').click()
            check('font-minus', page.evaluate('getComputedStyle(document.documentElement).getPropertyValue("--font")') == before)
            copy = page.locator('.copy').first
            expected = copy.locator('xpath=ancestor::div[contains(@class,"codewrap")]').locator('code').inner_text()
            copy.click()
            page.wait_for_timeout(100)
            check('copy-button-reports-native-success', bool(expected) and copy.inner_text() == '已复制', 'Native clipboard/execCommand path ran; OS clipboard content not read back.')
            # Print CSS exposes all chapters even when a screen filter was active.
            page.locator('#search').fill('md-container')
            page.emulate_media(media='print')
            print_visible = page.locator('.chapter[data-chapter]:visible').count()
            check('print-all-chapters', print_visible == original_count)
            check('print-hides-navigation', not page.locator('.sidebar').is_visible())
            page.emulate_media(media='screen')
            page.locator('#search').fill('')
            page.set_viewport_size({'width': 390, 'height': 844})
            load(guide)
            check('mobile-menu-initially-closed', not page.locator('.sidebar').is_visible())
            page.locator('#menu').click()
            check('mobile-menu-opens', page.locator('.sidebar').is_visible())
            page.locator('#search').fill('生产')
            check('mobile-filter', 0 < page.locator('.chapter[data-chapter]:visible').count() < page.locator('.chapter[data-chapter]').count())
            page.locator('#search').fill('')
            page.locator('#menu').click()
            load(guide, 'G08')
            page.wait_for_timeout(100)
            page.screenshot(path=str(shots / 'core-flow-mobile.png'), full_page=False)
            report['screenshots'].append('review/screenshots/core-flow-mobile.png')
            check('no-javascript-errors', not errors, errors)
            check('no-external-assets-requested', not external, external)
            browser.close()
    except Exception as exc:
        check('browser-run-completed', False, f'{type(exc).__name__}: {exc}')
    report['status'] = 'PASS' if report['checks'] and all(x['status'] == 'PASS' for x in report['checks']) else 'FAIL'
    report['counts'] = {'checks': len(report['checks']), 'screenshots': len(report['screenshots'])}
    save()
    print(json.dumps({'status': report['status'], 'counts': report['counts'],
                      'failures': [x for x in report['checks'] if x['status'] != 'PASS']}, ensure_ascii=False))
    if report['status'] != 'PASS':
        raise SystemExit(1)

if __name__ == '__main__':
    main()
