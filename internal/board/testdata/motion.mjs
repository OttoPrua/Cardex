import { readFileSync } from "node:fs";
import vm from "node:vm";

const file = readFileSync(0, "utf8");
const source = [...file.matchAll(/<script>([\s\S]*?)<\/script>/g)].map((m) => m[1]).find((script) => script.includes("function switchView"));
if (!source) throw new Error("board motion entry point missing");

function scenario(reduce, open) {
  let elapsed = 0;
  const records = [], raf = [], timers = new Map();
  let timerID = 0, click;
  const root = { dataset: { view: "status", theme: "light", ...(open ? {open:"1"} : {}) } };
  const make = (kind, i = 0) => ({
    kind, style: {}, classList: {add() {}, remove() {}}, getAnimations() { return []; },
    getBoundingClientRect() { return {left:30+i*250, top:50, width:240, height:200}; },
    animate(_frames, options) { records.push({kind, start:options.delay ?? 0, end: (options.delay ?? 0) + options.duration}); return {cancel(){}}; }
  });
  const folder = (i) => {
    const el = make("folder", i);
    const cards = Array.from({length: 12}, (_, k) => make("card", k));
    el.querySelector = (sel) => sel === ".of-shadow" ? make("shadow") : sel === ".of-tab" ? make("tab") : null;
    el.querySelectorAll = (sel) => sel === ".icard3" ? cards : [];
    return el;
  };
  const panel = (name, n) => ({...make("panel"), name, offsetHeight:300, querySelectorAll(sel) { return sel === ".of" ? this.folders : []; }, folders:Array.from({length:n}, (_,i)=>folder(i))});
  const plans = {status:panel("status",7), project:panel("project",12)};
  const board = {
    ...make("board"), offsetParent: {}, clientWidth:1100, offsetHeight:300, offsetWidth:1100,
    querySelectorAll(sel) { return sel === ".board-view" ? Object.values(plans) : sel === ".of" ? Object.values(plans).flatMap(p=>p.folders) : []; },
    querySelector(sel) { return sel === '[data-panel="status"]' ? plans.status : sel === '[data-panel="project"]' ? plans.project : null; }
  };
  const pane = {name:"pane"};
  const selector = {...make("selector"), getAttribute(){ return "?view=project"; }, setAttribute(){}, addEventListener(key,handler){ if(key==="click") click=handler; }};
  const document = {
    documentElement: root, getElementById(id){ return id==="board"?board:id==="pane-detail"?pane:null; },
    querySelectorAll(sel){ return sel === ".cx-seg a" ? [selector] : []; }
  };
  const ctx = {
    document, window: {}, CSS:{supports(){return true;}}, matchMedia(){return {matches:reduce};},
    sessionStorage:{setItem(){}}, localStorage:{setItem(){}}, URLSearchParams,
    location:{pathname:"/", search:"?view=status", hash:""}, history:{replaceState(){}},
    performance:{now(){return elapsed;}}, getComputedStyle(el){ return {display: el.name === "project" && root.dataset.view!=="project" ? "none" : "block"}; },
    clearTimeout(id){timers.delete(id);}, setTimeout(cb,delay){const key=++timerID; timers.set(key,{cb,delay});return key;},
    requestAnimationFrame(cb){raf.push(cb);}
  };
  vm.runInNewContext(source, ctx);
  while (raf.length) { raf.shift()(elapsed); }
  if (!open) click({preventDefault(){}});
  while (raf.length) { elapsed+=16; raf.shift()(elapsed); }
  for (const {cb, delay} of [...timers.values()].sort((a,b)=>a.delay-b.delay)) { elapsed=delay; cb(); }
  if (root.dataset.open || root.dataset.switching) throw new Error("transient flag not cleaned");
  const result={end:Math.max(0,...records.map(o=>o.end)), cleanup:elapsed, keyframeAnimations:records.length};
  if (open) result.lastCard=Math.max(0, ...records.filter(o=>o.kind==="card").map(o=>o.end));
  return result;
}
const out = {
  switch: scenario(false, false),
  open: scenario(false, true),
  reduced: scenario(true, false),
  reducedOpen: scenario(true, true)
};
console.log(JSON.stringify(out));
if (out.switch.end !== 914 || out.switch.cleanup !== 914) throw new Error("switch timing mismatch");
if (out.open.end !== 720 || out.open.lastCard !== 720 || out.open.cleanup !== 720) throw new Error("opening timing mismatch");
if (out.reduced.keyframeAnimations !== 0 || out.reduced.cleanup !== 160) throw new Error("reduced motion mismatch");
if (out.reducedOpen.keyframeAnimations !== 0) throw new Error("reduced opening still animated");
