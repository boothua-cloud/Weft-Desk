import { createApp } from 'vue'
import App from './App.vue'
import './styles.css'
import { applyTheme, themeMode, watchSystemTheme } from './theme'

applyTheme(themeMode())
watchSystemTheme()

createApp(App).mount('#app')
