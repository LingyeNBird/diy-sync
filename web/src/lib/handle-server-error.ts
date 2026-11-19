import { toast } from 'sonner'
import i18n from '@/i18n'

export function handleServerError(error: unknown) {
  toast.error(i18n.t('errors.actionFailed'), { description: error instanceof Error ? error.message : undefined })
}
