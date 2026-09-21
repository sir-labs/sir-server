"""Authenticated public UI smoke and isolated rendering/error fixtures."""
import json,os,pathlib,tempfile
from playwright.sync_api import sync_playwright
base=os.getenv('STATUS_URL','https://proxy.sir-labs.com').rstrip('/')
token=os.environ['SIR_PAT']
out=pathlib.Path(os.getenv('STATUS_EVIDENCE_DIR',tempfile.mkdtemp(prefix='sir-logs-')));out.mkdir(parents=True,exist_ok=True)
with sync_playwright() as p:
 browser=p.chromium.launch(headless=True,executable_path=os.getenv('PLAYWRIGHT_CHROMIUM_EXECUTABLE'),args=['--no-sandbox'])
 page=browser.new_page(viewport={'width':1440,'height':1080})
 errors=[];page.on('pageerror',lambda e:errors.append(str(e)))
 page.route(base+'/**',lambda r:r.continue_(headers={**r.request.headers,'Authorization':'Bearer '+token}))
 page.goto(base+'/')
 page.wait_for_function("document.querySelectorAll('[data-node]').length > 0")
 page.locator('[data-node="sir-nginx"]').click()
 page.get_by_role('tab',name='Logs',exact=True).click()
 page.wait_for_function("document.querySelectorAll('.log-line').length > 0")
 if os.getenv('LOG_ROTATION_CHECK') == '1':
  with page.expect_request(lambda req: '/api/logs?' in req.url and bool(req.headers.get('last-event-id')), timeout=75000):
   pass
  page.wait_for_function("document.querySelector('#log-status').textContent.includes('เชื่อมต่อแล้ว')")
  print('PASS live connection rotation with timestamp resume')
 page.get_by_role('button',name='หยุด Live',exact=True).click()
 assert 'หยุด Live แล้ว' in page.locator('#log-status').inner_text()
 page.locator('#log-autoscroll').uncheck()
 page.locator('#log-search').fill('definitely-no-matching-log-71924')
 assert page.locator('.log-line').count()==0
 page.locator('#log-search').fill('')
 assert page.locator('.log-line').count()>0
 page.locator('#log-tail').select_option('100')
 page.wait_for_function("document.querySelector('#log-status').textContent.includes('ครบแล้ว')")
 assert page.locator('.log-line').count()<=100
 # Metric refresh must preserve the selected Logs tab and its buffer.
 page.locator('#refresh').click()
 assert page.locator('#log-panel').is_visible()
 assert page.locator('#detail').is_hidden()
 # Use synthetic logs for screenshots, so evidence contains no real operational logs.
 record={'time':'2026-09-21T00:00:00Z','stream':'stderr','text':'<img src=x onerror=alert(1)> example failure; token=[REDACTED]'}
 fixture='event: meta\ndata: {}\n\nevent: log\ndata: '+json.dumps(record)+'\n\nevent: end\ndata: {}\n\n'
 page.route('**/api/logs?*',lambda r:r.fulfill(status=200,content_type='text/event-stream',body=fixture))
 page.locator('#log-reload').click()
 page.wait_for_function("document.querySelector('#log-output').textContent.includes('example failure')")
 assert page.locator('#log-output img').count()==0
 assert page.locator('.log-line.stderr').count()==1
 page.locator('#log-panel').screenshot(path=str(out/'logs-desktop.png'))
 page.set_viewport_size({'width':390,'height':844})
 assert page.evaluate('document.documentElement.scrollWidth <= innerWidth')
 page.locator('#log-panel').screenshot(path=str(out/'logs-mobile.png'))
 page.route('**/api/logs?*',lambda r:r.fulfill(status=403,body='Administrator required'))
 page.locator('#log-reload').click()
 page.wait_for_function("document.querySelector('#log-status').textContent.includes('admin')")
 page.get_by_role('tab',name='Overview',exact=True).click()
 assert page.locator('#log-panel').is_hidden()
 assert not errors,errors
 (out/'logs-browser-checks.json').write_text(json.dumps({'passed':True,'checks':['real-live-output','pause','autoscroll-control','search','history-tail','refresh-preserves-tab','safe-text-rendering','stderr-label','mobile-no-overflow','admin-denial','close-tab'],'page_errors':errors},indent=2))
 browser.close()
 print('PASS log browser checks:',out)
