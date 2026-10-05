# Hooks de inicializacao do LocalStack.
#
# Os arquivos desta pasta sao executados uma vez, no start do LocalStack, e e o
# lugar onde as filas e a politica de redrive nascem por codigo.
#
# A ideia e deliberada: uma fila criada a mao no console e uma fila que o outro
# ambiente nao tem. Provisionar por script faz o teste de integracao depender
# apenas de `docker compose up -d`.
#
# O provisionamento esta em `00-filas.sh`. As filas `wager-transactions.fifo` e
# `wager-transactions-dlq.fifo` nascem de la, com a politica de redrive ja decided: FIFO,
# long polling de 20s, visibility timeout de 60s, tres tentativas antes da fila morta e
# redrivePermitida. A cartao morto e criada primeiro para que a ARN exista quando a
# politica da fila de operacoes e montada.
