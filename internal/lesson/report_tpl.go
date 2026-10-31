package lesson

const reportHTML = `<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<title>{{.Title}} · 课堂报告</title>
<style>
  :root{
    --ink:#22201d; --sub:#6b665d; --line:#e2ddd2; --paper:#faf8f3;
    --accent:#c96442; --accent-soft:#f3e6df; --card:#fffdf9;
  }
  *{box-sizing:border-box}
  body{
    margin:0; background:var(--paper); color:var(--ink);
    font-family:"PingFang SC","Microsoft YaHei",-apple-system,"Segoe UI",sans-serif;
    font-size:13.5px; line-height:1.75;
  }
  .wrap{max-width:820px;margin:0 auto;padding:52px 44px 64px}
  header{border-bottom:1px solid var(--line);padding-bottom:26px;margin-bottom:30px}
  .kicker{font-size:11px;letter-spacing:.22em;text-transform:uppercase;color:var(--accent);font-weight:600}
  h1{
    font-family:"Songti SC","Noto Serif SC",Georgia,serif;
    font-size:32px;line-height:1.3;margin:14px 0 18px;font-weight:600;letter-spacing:.01em;
  }
  .meta{display:flex;flex-wrap:wrap;gap:8px}
  .chip{
    border:1px solid var(--line);border-radius:999px;padding:3px 11px;font-size:11.5px;
    color:var(--sub);background:var(--card);
  }
  h2{
    font-family:"Songti SC","Noto Serif SC",Georgia,serif;
    font-size:19px;margin:38px 0 14px;font-weight:600;display:flex;align-items:center;gap:10px;
  }
  h2::before{content:"";width:3px;height:17px;background:var(--accent);border-radius:2px}
  .judge{
    background:var(--card);border:1px solid var(--line);border-radius:14px;
    padding:22px 24px;display:flex;gap:26px;align-items:center;
  }
  .score{font-family:Georgia,serif;font-size:46px;line-height:1;color:var(--accent);min-width:104px}
  .score small{font-size:13px;color:var(--sub);display:block;margin-top:6px;font-family:inherit}
  .judge-body{flex:1}
  .judge-title{font-weight:600;margin-bottom:4px;font-size:14.5px}
  .judge p{margin:0;color:var(--sub);font-size:12.8px}
  .grid{display:grid;grid-template-columns:1fr 1fr;gap:14px;margin-top:14px}
  .mini{background:var(--card);border:1px solid var(--line);border-radius:12px;padding:16px 18px}
  .mini h3{margin:0 0 8px;font-size:12px;letter-spacing:.12em;color:var(--sub);text-transform:uppercase;font-weight:600}
  .mini ul{margin:0;padding-left:17px}
  .mini li{margin-bottom:4px;font-size:12.6px}
  .page{
    background:var(--card);border:1px solid var(--line);border-radius:14px;
    padding:20px 22px;margin-bottom:18px;page-break-inside:avoid;
  }
  .page-head{display:flex;justify-content:space-between;align-items:baseline;margin-bottom:12px}
  .page-no{font-family:Georgia,serif;font-size:13px;color:var(--accent);letter-spacing:.06em}
  .page-time{font-size:11.5px;color:var(--sub)}
  .shot{width:100%;border-radius:9px;border:1px solid var(--line);margin:2px 0 14px;display:block}
  .summary{font-size:14px;font-weight:600;margin:0 0 8px}
  .notes{font-size:13px;color:#3a3630}
  .notes h3{font-size:13.5px;margin:12px 0 5px}
  .notes ul{padding-left:18px;margin:6px 0}
  .points{display:flex;flex-wrap:wrap;gap:6px;margin-top:12px}
  .point{background:var(--accent-soft);color:#8a4227;border-radius:6px;padding:2px 9px;font-size:11.5px}
  .tips{margin-top:10px;padding:10px 13px;border-left:2px solid var(--accent);background:#fbf6f2;font-size:12.4px;color:#5b5349}
  .copy-flag{margin-top:12px;font-size:12px;color:var(--sub);border-top:1px dashed var(--line);padding-top:10px}
  .copy-flag b{color:var(--accent)}
  .tr{font-size:12.2px;color:#4a453d;margin-bottom:12px}
  .tr b{color:var(--accent);font-weight:600;margin-right:8px}
  footer{margin-top:44px;border-top:1px solid var(--line);padding-top:16px;font-size:11px;color:var(--sub);display:flex;justify-content:space-between}
  @media print{
    body{background:#fff}
    .wrap{padding:0 6mm;max-width:none}
    .page,.judge,.mini{break-inside:avoid}
    h2{break-after:avoid}
  }
</style>
</head>
<body>
<div class="wrap">
  <header>
    <div class="kicker">课堂报告 · CLASS NOTE</div>
    <h1>{{.Title}}</h1>
    <div class="meta">
      <span class="chip">{{.Subject}}</span>
      <span class="chip">{{.Date}}</span>
      <span class="chip">{{.TimeRange}}</span>
      <span class="chip">时长 {{.Duration}}</span>
      <span class="chip">{{.Slides}} 个片段</span>
      <span class="chip">{{.Words}} 字转写</span>
    </div>
  </header>

  <h2>教师讲授方式判断</h2>
  <div class="judge">
    <div class="score">{{.CopyRate}}%<small>照搬程度</small></div>
    <div class="judge-body">
      <div class="judge-title">{{.CopyLabel}}</div>
      <p>{{.Summary}}</p>
    </div>
  </div>

  {{if .Highlights}}
  <div class="grid">
    <div class="mini">
      <h3>课堂亮点</h3>
      <ul>{{range .Highlights}}<li>{{.}}</li>{{end}}</ul>
    </div>
    <div class="mini">
      <h3>可改进之处</h3>
      <ul>{{range .Problems}}<li>{{.}}</li>{{end}}</ul>
    </div>
  </div>
  {{end}}

  {{if .Suggestions}}
  <div class="mini" style="margin-top:14px">
    <h3>改进建议</h3>
    <ul>{{range .Suggestions}}<li>{{.}}</li>{{end}}</ul>
  </div>
  {{end}}

  <h2>逐页笔记</h2>
  {{range .Pages}}
  <div class="page">
    <div class="page-head">
      <span class="page-no">PAGE {{.Index}}{{if .SlideIndex}} · 幻灯片 {{.SlideIndex}}{{end}}</span>
      <span class="page-time">{{.TimeLabel}}</span>
    </div>
    {{if .Image}}<img class="shot" src="{{.Image}}" alt="第 {{.Index}} 页">{{end}}
    {{if .Summary}}<p class="summary">{{.Summary}}</p>{{end}}
    {{if .Notes}}<div class="notes">{{nl2br .Notes}}</div>{{end}}
    {{if .KeyPoints}}
    <div class="points">{{range .KeyPoints}}<span class="point">{{.}}</span>{{end}}</div>
    {{end}}
    {{if .Tips}}<div class="tips">提示：{{range $i, $t := .Tips}}{{if $i}} · {{end}}{{$t}}{{end}}</div>{{end}}
    {{if .Evidence}}<div class="copy-flag">照搬评分 <b>{{.CopyScore}}</b> · {{.Evidence}}</div>{{end}}
  </div>
  {{end}}

  {{if .Transcript}}
  <h2>课堂实录摘录</h2>
  {{range .Transcript}}
  <div class="tr"><b>P{{.Index}}</b>{{.Text}}</div>
  {{end}}
  {{end}}

  <footer>
    <span>由 ClassNote 自动生成 · {{.Model}}</span>
    <span>{{.Date}}</span>
  </footer>
</div>
</body>
</html>`
