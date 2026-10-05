# Hooks de inicializacao do LocalStack.
#
# Os arquivos desta pasta sao executados uma vez, no start do LocalStack, e e o
# lugar onde as filas e a politica de redrive nascem por codigo.
#
# A ideia e deliberada: uma fila criada a mao no console e uma fila que o outro
# ambiente nao tem. Provisionar por script faz o teste de integracao depender
# apenas de `docker compose up -d`.
#
# O provisionamento esta em `00-filas.sh`. Quatro filas nascem de la, todas FIFO com a
# politica de redrive ja decidida: long polling de 20s, visibility timeout de 60s,
# tres tentativas antes da fila morta e redrivePermitida.
#
# Duas sao de entrada: `wager-transactions.fifo`, que recebe comando de jogo do
# provedor, e `wager-transactions-dlq.fifo`, sua cartao morto.
#
# Duas sao de saida: `wager-events.fifo`, que recebe o envelope publicado pelo relay
# da outbox, e `wager-events-dlq.fifo`. A chave de particao da saida e o agregado do
# evento; ver a secao "Inbox e outbox" do `ARCHITECTURE.md`.
#
# Cada cartao morto e criada antes da fila que a referencia, para que a ARN exista
# quando a politica de redrive e montada.
