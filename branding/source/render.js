const {Resvg}=require('@resvg/resvg-js');const fs=require('fs');
const [,,inp,out,w,bg]=process.argv;
const r=new Resvg(fs.readFileSync(inp),{fitTo:{mode:'width',value:+w},background:bg||undefined,font:{loadSystemFonts:false}});
fs.writeFileSync(out,r.render().asPng());
