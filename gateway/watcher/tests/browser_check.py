import json,pathlib,os,tempfile
base=os.getenv("STATUS_URL", "http://127.0.0.1:8080").rstrip("/")
from playwright.sync_api import sync_playwright
out=pathlib.Path(os.getenv('STATUS_EVIDENCE_DIR', tempfile.mkdtemp(prefix='sir-status-')));out.mkdir(parents=True,exist_ok=True)
with sync_playwright() as p:
 b=p.chromium.launch(headless=True,executable_path=os.getenv('PLAYWRIGHT_CHROMIUM_EXECUTABLE'),args=['--no-sandbox'])
 page=b.new_page(viewport={'width':1440,'height':1100},device_scale_factor=1)
 token=os.getenv('SIR_PAT','')
 if token and base.startswith('https://'):
  page.route(base+'/**',lambda r:r.continue_(headers={**r.request.headers,'Authorization':'Bearer '+token}))
 errors=[];page.on('pageerror',lambda e:errors.append(str(e)))
 page.goto(base+'/')
 page.wait_for_function("document.querySelectorAll('#services tr').length > 5")
 assert page.locator('#checks .passed').count()==4
 assert page.locator('#flow .node').count()==9
 page.locator('[data-node="sir-mcp-mcp-1"]').click()
 assert 'Gateway → MCP' in page.locator('#detail').inner_text()
 page.locator('#search').fill('sir-mcp')
 assert page.locator('#services tr').count()==1
 page.locator('#search').fill('')
 page.locator('#flowScope').select_option('all')
 assert page.locator('#flow .node').count()>=page.locator('#services tr').count()
 page.locator('#flowScope').select_option('core')
 page.screenshot(path=str(out/'desktop.png'),full_page=True)
 page.set_viewport_size({'width':390,'height':844})
 assert page.evaluate('document.documentElement.scrollWidth <= window.innerWidth')
 page.screenshot(path=str(out/'mobile.png'),full_page=True)
 # Browser-visible fixture verifies failure and staleness without stopping real services.
 snapshot=page.request.get(base+'/api/status',headers={'Authorization':'Bearer '+token} if token else {}).json()
 snapshot['sampled_at']='2020-01-01T00:00:00Z'
 snapshot['nodes'][0]['status']='down';snapshot['nodes'][0]['state']='exited'
 snapshot['checks'][2]['status']='failed';snapshot['checks'][2]['detail']='HTTP 401'
 page.route('**/api/status',lambda r:r.fulfill(json=snapshot))
 page.locator('#refresh').click()
 page.wait_for_function("document.body.classList.contains('stale')")
 assert page.locator('#banner').is_visible()
 assert page.locator('#checks .failed').count()==1
 page.screenshot(path=str(out/'stale-failure.png'),full_page=True)
 assert not errors,errors
 (out/'browser-checks.json').write_text(json.dumps({'passed':True,'checks':['desktop','mobile-no-page-overflow','node-details','service-filter','all-service-flow','stale-banner','failed-probe-visible'],'js_errors':errors},indent=2))
 b.close()
 print('PASS browser checks; screenshots:',out)
