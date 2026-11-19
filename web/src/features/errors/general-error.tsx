import { ErrorPage } from './error-page'

type GeneralErrorProps = React.HTMLAttributes<HTMLDivElement> & { minimal?: boolean }

export function GeneralError({ className, minimal = false }: GeneralErrorProps) {
  return <ErrorPage code='500' kind='general' minimal={minimal} className={className} />
}
