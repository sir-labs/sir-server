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
 page.wait_for_function("document.querySelectorAll('#checks .passed').length === 4")
 assert page.locator('#checks .passed').count()==4
 assert page.locator('#flow .node').count()==9
 def assert_edges_aligned():
  page.wait_for_function('''() => {
   const flow=document.querySelector('#flow').getBoundingClientRect();
   const view=document.querySelector('#edges').viewBox.baseVal;
   return Math.abs(view.width-flow.width)<1 && Math.abs(view.height-flow.height)<1;
  }''')
  result=page.evaluate('''() => {
   const flow=document.querySelector('#flow'), svg=document.querySelector('#edges');
   const box=flow.getBoundingClientRect(), view=svg.viewBox.baseVal;
   const bad=[],wrongAnchors=[];
   for(const path of svg.querySelectorAll('path:not(marker path)')){
    const length=path.getTotalLength();
    const ends=[path.getPointAtLength(0),path.getPointAtLength(length)];
    for(const point of ends){
     const screen=new DOMPoint(point.x,point.y).matrixTransform(path.getScreenCTM());
     const touches=[...flow.querySelectorAll('.node')].some(node=>{
      const n=node.getBoundingClientRect(), pad=4;
      return screen.x>=n.left-pad&&screen.x<=n.right+pad&&screen.y>=n.top-pad&&screen.y<=n.bottom+pad;
     });
     if(!touches)bad.push({x:screen.x,y:screen.y});
    }
    const source=flow.querySelector(`[data-node="${CSS.escape(path.dataset.from)}"]`).getBoundingClientRect();
    const target=flow.querySelector(`[data-node="${CSS.escape(path.dataset.to)}"]`).getBoundingClientRect();
    const centers={source:{x:source.x+source.width/2,y:source.y+source.height/2},target:{x:target.x+target.width/2,y:target.y+target.height/2}};
    const sameColumn=Math.abs(centers.source.x-centers.target.x)<Math.min(source.width,target.width)/2;
    const expected=sameColumn
      ? [{x:centers.source.x,y:centers.source.y<centers.target.y?source.bottom:source.top},{x:centers.target.x,y:centers.source.y<centers.target.y?target.top:target.bottom}]
      : [{x:centers.source.x<centers.target.x?source.right:source.left,y:centers.source.y},{x:centers.source.x<centers.target.x?target.left:target.right,y:centers.target.y}];
    const actual=ends.map(point=>new DOMPoint(point.x,point.y).matrixTransform(path.getScreenCTM()));
    const distance=(a,b)=>Math.hypot(a.x-b.x,a.y-b.y);
    if(distance(actual[0],expected[0])>3||distance(actual[1],expected[1])>3)wrongAnchors.push({from:path.dataset.from,to:path.dataset.to,actual,expected});
   }
   return {bad,paths:svg.querySelectorAll('path:not(marker path)').length,
    wrongAnchors,viewBox:[view.width,view.height],flow:[box.width,box.height]};
  }''')
  assert result['paths']>=6,result
  assert abs(result['viewBox'][0]-result['flow'][0])<1,result
  assert abs(result['viewBox'][1]-result['flow'][1])<1,result
  assert not result['bad'],result
  assert not result['wrongAnchors'],result
 assert_edges_aligned()
 page.locator('[data-node="sir-mcp-mcp-1"]').click()
 assert 'Gateway → MCP' in page.locator('#detail').inner_text()
 page.locator('#search').fill('sir-mcp')
 assert page.locator('#services tr').count()==1
 page.locator('#search').fill('')
 page.locator('#flowScope').select_option('all')
 assert page.locator('#flow .node').count()>=page.locator('#services tr').count()
 assert_edges_aligned()
 page.locator('#flowScope').select_option('core')
 assert_edges_aligned()
 page.screenshot(path=str(out/'desktop.png'),full_page=True)
 page.set_viewport_size({'width':390,'height':844})
 assert page.evaluate('document.documentElement.scrollWidth <= window.innerWidth')
 assert_edges_aligned()
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
 (out/'browser-checks.json').write_text(json.dumps({'passed':True,'checks':['desktop','mobile-no-page-overflow','arrow-endpoint-alignment','arrow-layout-resize','node-details','service-filter','all-service-flow','stale-banner','failed-probe-visible'],'js_errors':errors},indent=2))
 b.close()
 print('PASS browser checks; screenshots:',out)
