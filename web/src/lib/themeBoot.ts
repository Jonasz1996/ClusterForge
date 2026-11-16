// themeBootScript zet het gekozen thema vóór de eerste weergave, zodat de
// pagina niet eerst licht en dan donker flitst. Los van theme.ts omdat de
// root-layout geen client-module mag importeren voor een gewone string.
export const themeKey = "cf-theme";

export const themeBootScript = `try{var t=localStorage.getItem("${themeKey}");if(t==="light"||t==="dark")document.documentElement.setAttribute("data-theme",t)}catch(e){}`;
