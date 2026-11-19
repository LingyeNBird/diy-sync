import i18n from 'i18next'
import LanguageDetector from 'i18next-browser-languagedetector'
import { initReactI18next } from 'react-i18next'
import en from './locales/en'
import zhCN from './locales/zh-CN'

export const languages = [
  { code: 'en', label: 'English' },
  { code: 'zh-CN', label: '简体中文' },
] as const

export type LanguageCode = (typeof languages)[number]['code']

void i18n
  .use(LanguageDetector)
  .use(initReactI18next)
  .init({
    resources: {
      en: { translation: en },
      'zh-CN': { translation: zhCN },
    },
    fallbackLng: 'en',
    supportedLngs: ['en', 'zh-CN'],
    // "zh", "zh-Hans", "zh-SG" … all fall back to zh-CN.
    nonExplicitSupportedLngs: false,
    load: 'currentOnly',
    detection: {
      order: ['localStorage', 'navigator'],
      lookupLocalStorage: 'fdsync-lang',
      caches: ['localStorage'],
      convertDetectedLanguage: (lng: string) => (lng.toLowerCase().startsWith('zh') ? 'zh-CN' : lng),
    },
    interpolation: { escapeValue: false },
    returnNull: false,
  })

i18n.on('languageChanged', (lng) => {
  document.documentElement.lang = lng
})

export default i18n

/**
 * Untyped lookup for keys built at runtime from server data (event codes,
 * exclusion reasons) whose parameters are free-form.
 */
export const tx = (key: string, params?: Record<string, unknown>): string =>
  (i18n.t as unknown as (k: string, o?: object) => string)(key, params)
